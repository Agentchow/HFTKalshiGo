package execution

import (
	"context"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

var _ OrderPlacer = (*kalshi_http.Client)(nil)

// Service subscribes to OrderIntent events, applies risk checks via the
// lane router, and places orders through the Kalshi batch HTTP endpoint.
//
// Order placement is async — the HTTP call runs on a short-lived goroutine
// so it never blocks the game's event loop. The fill result is fed back
// to the game's goroutine via gc.Send().
type Service struct {
	bus       *events.Bus
	router    *LaneRouter
	client    OrderPlacer
	gameStore *store.GameStateStore
}

func NewService(bus *events.Bus, router *LaneRouter, client OrderPlacer, gameStore *store.GameStateStore) *Service {
	s := &Service{
		bus:       bus,
		router:    router,
		client:    client,
		gameStore: gameStore,
	}

	bus.Subscribe(events.EventOrderIntent, s.onOrderIntent)

	return s
}

// onOrderIntent is called on the game's goroutine (via the synchronous bus).
// The payload is now []OrderIntent (a batch). It checks per-game and per-sport
// spending caps and dedup for each intent, then spawns a goroutine for the
// batch HTTP call.
func (s *Service) onOrderIntent(evt events.Event) error {
	intents, ok := evt.Payload.([]events.OrderIntent)
	if !ok {
		return nil
	}

	var approved []events.OrderIntent
	for _, intent := range intents {
		lane := s.router.Route(intent.Sport, intent.League)
		if lane == nil {
			telemetry.Warnf("execution: no lane for sport=%s league=%s", intent.Sport, intent.League)
			continue
		}

		orderCents := int(intent.LimitPct)

		gc, gcOK := s.gameStore.Get(intent.Sport, intent.GameID)
		if gcOK && lane.MaxGameCents() > 0 {
			if gc.TotalExposureCents()+orderCents > lane.MaxGameCents() {
				telemetry.Infof("execution: per-game cap reached ticker=%s game=%s spent=%d limit=%d",
					intent.Ticker, intent.GameID, gc.TotalExposureCents(), lane.MaxGameCents())
				continue
			}
		}

		if !lane.Allow(intent.Ticker, intent.HomeScore, intent.AwayScore, orderCents) {
			telemetry.Infof("execution: blocked by lane checks ticker=%s", intent.Ticker)
			continue
		}

		lane.RecordOrder(intent.Ticker, intent.HomeScore, intent.AwayScore, orderCents)
		approved = append(approved, intent)
	}

	if len(approved) == 0 {
		return nil
	}

	go s.placeBatchOrder(approved, evt.Timestamp)
	return nil
}

func (s *Service) placeBatchOrder(intents []events.OrderIntent, webhookReceivedAt time.Time) {
	var reqs []kalshi_http.CreateOrderRequest
	for _, intent := range intents {
		req := kalshi_http.CreateOrderRequest{
			Ticker:      intent.Ticker,
			Action:      "buy",
			Side:        intent.Side,
			Type:        "limit",
			Count:       1,
			ClientID:    intent.Ticker + ":" + intent.Reason,
			TimeInForce: "good_till_canceled",
		}
		// TODO: remove hardcoded 1¢ limit once strategy pricing is validated
		if intent.Side == "yes" {
			req.YesPrice = 1
		} else {
			req.NoPrice = 1
		}
		reqs = append(reqs, req)
	}

	if !webhookReceivedAt.IsZero() {
		e2e := time.Since(webhookReceivedAt)
		telemetry.Metrics.OrderE2ELatency.Record(e2e)
		telemetry.Infof("execution: batch e2e_latency=%s orders=%d", e2e, len(reqs))
	}

	resp, err := s.client.PlaceBatchOrders(context.Background(), kalshi_http.BatchCreateOrdersRequest{
		Orders: reqs,
	})
	if err != nil {
		telemetry.Errorf("execution: batch order failed: %v", err)
		return
	}

	for i, r := range resp.Orders {
		if i >= len(intents) {
			break
		}
		intent := intents[i]

		if r.Error != nil {
			telemetry.Errorf("execution: order failed ticker=%s: %s", intent.Ticker, r.Error.Message)
			continue
		}
		if r.Order == nil {
			continue
		}

		telemetry.Infof("execution: order placed ticker=%s side=%s order_id=%s reason=%q",
			intent.Ticker, intent.Side, r.Order.OrderID, intent.Reason)

		gc, ok := s.gameStore.Get(intent.Sport, intent.GameID)
		if !ok {
			continue
		}
		orderID := r.Order.OrderID
		gc.Send(func() {
			gc.RecordFill(game.Fill{
				OrderID:   orderID,
				Ticker:    intent.Ticker,
				Side:      intent.Side,
				CostCents: 1,
			})
		})
	}
}
