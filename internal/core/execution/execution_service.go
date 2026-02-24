package execution

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/core/execution/lanes"
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

	// Resolve team names once for the whole batch
	matchLabel := "? vs ?"
	if gc, ok := s.gameStore.Get(intents[0].Sport, intents[0].GameID); ok {
		matchLabel = fmt.Sprintf("%s vs %s",
			shortName(gc.Game.GetHomeTeam()), shortName(gc.Game.GetAwayTeam()))
	}

	var approved []events.OrderIntent
	for _, intent := range intents {
		lane := s.router.Route(intent.Sport, intent.League)
		if lane == nil {
			telemetry.Warnf("[RISK-LIMIT] %s — no lane configured for %s/%s",
				matchLabel, intent.Sport, intent.League)
			continue
		}

		orderCents := int(intent.LimitPct)

		gc, gcOK := s.gameStore.Get(intent.Sport, intent.GameID)
		if gcOK && lane.MaxGameCents() > 0 {
			spent := gc.TotalExposureCents()
			if spent+orderCents > lane.MaxGameCents() {
				telemetry.Infof("[RISK-LIMIT] %s — per-game cap (%d/%d¢ spent)",
					matchLabel, spent, lane.MaxGameCents())
				continue
			}
		}

		reason := lane.Check(intent.Ticker, intent.HomeScore, intent.AwayScore, orderCents)
		if reason != "" && !(reason == lanes.RejectDuplicate && intent.Overturn) {
			telemetry.Infof("[RISK-LIMIT] %s — %s (score %d-%d)",
				matchLabel, reason, intent.HomeScore, intent.AwayScore)
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
	homeTeam, awayTeam := "?", "?"
	gc, gcOK := s.gameStore.Get(intents[0].Sport, intents[0].GameID)
	if gcOK {
		homeTeam = shortName(gc.Game.GetHomeTeam())
		awayTeam = shortName(gc.Game.GetAwayTeam())
	}

	teamFor := func(outcome string) string {
		switch outcome {
		case "home":
			return homeTeam
		case "away":
			return awayTeam
		default:
			return outcome
		}
	}

	nameWidth := len(homeTeam)
	if len(awayTeam) > nameWidth {
		nameWidth = len(awayTeam)
	}

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
		if intent.Side == "yes" {
			req.YesPrice = 1
		} else {
			req.NoPrice = 1
		}
		reqs = append(reqs, req)
	}

	if !webhookReceivedAt.IsZero() {
		telemetry.Metrics.OrderE2ELatency.Record(time.Since(webhookReceivedAt))
	}

	// ── ORDER block ──
	ts := time.Now().Format("3:04:05.000 PM")
	tsPrefix := fmt.Sprintf("[%s] ", ts)
	pad := strings.Repeat(" ", len(tsPrefix))

	var ob strings.Builder
	for i, intent := range intents {
		prefix := pad
		if i == 0 {
			prefix = tsPrefix
		}
		price := reqs[i].YesPrice
		if intent.Side == "no" {
			price = reqs[i].NoPrice
		}
		fmt.Fprintf(&ob, "%s[ORDER] %-*s  %-3s  %d contracts @ %d¢\n",
			prefix, nameWidth, teamFor(intent.Outcome),
			strings.ToUpper(intent.Side), reqs[i].Count, price)
	}
	fmt.Fprint(os.Stderr, ob.String())

	resp, err := s.client.PlaceBatchOrders(context.Background(), kalshi_http.BatchCreateOrdersRequest{
		Orders: reqs,
	})
	if err != nil {
		telemetry.Errorf("execution: batch FAILED: %v", err)
		return
	}

	// ── RESPONSE block ──
	ts = time.Now().Format("3:04:05.000 PM")
	tsPrefix = fmt.Sprintf("[%s] ", ts)
	pad = strings.Repeat(" ", len(tsPrefix))

	var rb strings.Builder
	for i, r := range resp.Orders {
		if i >= len(intents) {
			break
		}
		intent := intents[i]
		name := teamFor(intent.Outcome)
		side := strings.ToUpper(intent.Side)

		prefix := pad
		if i == 0 {
			prefix = tsPrefix
		}

		if r.Error != nil {
			fmt.Fprintf(&rb, "%s[RESPONSE] %-*s  %-3s  REJECTED: %s\n",
				prefix, nameWidth, name, side, r.Error.Message)
			continue
		}
		if r.Order == nil {
			continue
		}

		o := r.Order
		total := o.FillCount + o.RemainingCount
		fillCost := o.TakerFillCost + o.MakerFillCost
		fees := o.TakerFees + o.MakerFees

		if o.FillCount > 0 {
			avg := float64(fillCost+fees) / float64(o.FillCount)
			fmt.Fprintf(&rb, "%s[RESPONSE] %-*s  %-3s  [%d/%d] %.1f¢ avg price\n",
				prefix, nameWidth, name, side, o.FillCount, total, avg)
		} else {
			fmt.Fprintf(&rb, "%s[RESPONSE] %-*s  %-3s  [0/%d] resting\n",
				prefix, nameWidth, name, side, total)
		}

		if !gcOK {
			continue
		}
		orderID := o.OrderID
		costCents := fillCost + fees
		gc.Send(func() {
			gc.RecordFill(game.Fill{
				OrderID:   orderID,
				Ticker:    intent.Ticker,
				Side:      intent.Side,
				CostCents: costCents,
			})
		})
	}
	fmt.Fprint(os.Stderr, rb.String())
}

func shortName(name string) string {
	i := strings.LastIndexByte(name, ' ')
	if i >= 0 {
		return name[i+1:]
	}
	return name
}
