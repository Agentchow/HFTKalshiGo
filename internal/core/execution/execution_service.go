package execution

import (
	"context"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Service subscribes to OrderIntent events, applies risk checks via the
// lane router, and places orders through the Kalshi HTTP client.
//
// Order placement is async — the HTTP call runs on a short-lived goroutine
// so it never blocks the game's event loop. The fill result is fed back
// to the game's goroutine via gc.Send().
type Service struct {
	bus       *events.Bus
	router    *LaneRouter
	client    *kalshi_http.Client
	gameStore *store.GameStateStore
}

func NewService(bus *events.Bus, router *LaneRouter, client *kalshi_http.Client, gameStore *store.GameStateStore) *Service {
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
// It checks per-game and per-sport spending caps, throttle, and dedup,
// then spawns a goroutine for the HTTP call.
func (s *Service) onOrderIntent(evt events.Event) error {
	intent, ok := evt.Payload.(events.OrderIntent)
	if !ok {
		return nil
	}

	lane := s.router.Route(intent.Sport, intent.League)
	if lane == nil {
		telemetry.Warnf("execution: no lane for sport=%s league=%s", intent.Sport, intent.League)
		return nil
	}

	orderCents := int(intent.LimitPct)

	// Per-game spending cap (safe to read — we're on the game's goroutine).
	gc, gcOK := s.gameStore.Get(intent.Sport, intent.GameID)
	if gcOK && lane.MaxGameCents() > 0 {
		if gc.TotalExposureCents()+orderCents > lane.MaxGameCents() {
			telemetry.Infof("execution: per-game cap reached ticker=%s game=%s spent=%d limit=%d",
				intent.Ticker, intent.GameID, gc.TotalExposureCents(), lane.MaxGameCents())
			return nil
		}
	}

	// Per-sport spending cap + throttle + idempotency.
	if !lane.Allow(intent.Ticker, intent.HomeScore, intent.AwayScore, orderCents) {
		telemetry.Infof("execution: blocked by lane checks ticker=%s", intent.Ticker)
		return nil
	}

	// Mark as sent before the async call so duplicate intents are rejected.
	lane.RecordOrder(intent.Ticker, intent.HomeScore, intent.AwayScore, orderCents)

	// Fire the HTTP call on its own goroutine — don't block the game's event loop.
	go s.placeOrder(intent)

	return nil
}

func (s *Service) placeOrder(intent events.OrderIntent) {
	req := kalshi_http.CreateOrderRequest{
		Ticker:   intent.Ticker,
		Action:   "buy",
		Side:     intent.Side,
		Type:     "limit",
		Count:    1,
		ClientID: intent.Ticker + ":" + intent.Reason,
	}

	if intent.Side == "yes" {
		req.YesPrice = int(intent.LimitPct)
	} else {
		req.NoPrice = int(intent.LimitPct)
	}

	resp, err := s.client.PlaceOrder(context.Background(), req)
	if err != nil {
		telemetry.Errorf("execution: order failed ticker=%s: %v", intent.Ticker, err)
		return
	}

	telemetry.Infof("execution: order placed ticker=%s order_id=%s reason=%q",
		intent.Ticker, resp.Order.OrderID, intent.Reason)

	gc, ok := s.gameStore.Get(intent.Sport, intent.GameID)
	if !ok {
		return
	}
	gc.Send(func() {
		gc.RecordFill(game.Fill{
			OrderID:   resp.Order.OrderID,
			Ticker:    intent.Ticker,
			Side:      intent.Side,
			CostCents: int(intent.LimitPct),
		})
	})
}
