package execution

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/core/execution/lanes"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/tracking"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

var _ OrderPlacer = (*kalshi_http.Client)(nil)

// Service subscribes to OrderIntent events, applies risk checks via the
// lane router, and places orders through the Kalshi batch HTTP endpoint.
//
// Order placement is async — the HTTP call runs on a short-LIVEd goroutine
// so it never blocks the game's event loop. The fill result is fed back
// to the game's goroutine via gc.Send().
type Service struct {
	bus       *events.Bus
	router    *LaneRouter
	client    OrderPlacer
	gameStore *store.GameStateStore
	tracker   *tracking.Tracker
	sessionID string
	orderSeq  int64
}

func NewService(bus *events.Bus, router *LaneRouter, client OrderPlacer, gameStore *store.GameStateStore, tracker *tracking.Tracker) *Service {
	s := &Service{
		bus:       bus,
		router:    router,
		client:    client,
		gameStore: gameStore,
		tracker:   tracker,
		sessionID: strconv.FormatInt(time.Now().UnixNano(), 36),
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

	// If this batch is from a confirmed overturn, clear all stale
	// idempotency entries for the affected tickers so that scores
	// seen before the overturn (e.g. the overturned 3-0) can be
	// re-ordered if they occur again legitimately.
	if intents[0].Overturn {
		cleared := make(map[string]bool)
		for _, intent := range intents {
			if cleared[intent.Ticker] {
				continue
			}
			if lane := s.router.Route(intent.Sport, intent.League); lane != nil {
				lane.ClearIdempotencyForTicker(intent.Ticker)
				cleared[intent.Ticker] = true
			}
		}
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

		reason := lane.Check(intent.Ticker, intent.Side, intent.HomeScore, intent.AwayScore, orderCents)
		if reason != "" && !(intent.Slam && reason == lanes.RejectDuplicate) {
			telemetry.Infof("[RISK-LIMIT] %s — %s (score %d-%d)",
				matchLabel, reason, intent.HomeScore, intent.AwayScore)
			continue
		}

		lane.RecordOrder(intent.Ticker, intent.Side, intent.HomeScore, intent.AwayScore, orderCents)
		approved = append(approved, intent)
	}

	if len(approved) == 0 {
		return nil
	}

	ttlSec := s.router.OrderTTL(approved[0].Sport)
	go s.placeBatchOrder(approved, evt.Timestamp, ttlSec)
	return nil
}

func (s *Service) placeBatchOrder(intents []events.OrderIntent, webhookReceivedAt time.Time, ttlSec int) {
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
	var kept []events.OrderIntent
	for _, intent := range intents {
		priceCents := math.Floor(intent.LimitPct)
		if strings.HasPrefix(intent.EID, "MOCK-") {
			priceCents = 1
		}
		if priceCents < 1 {
			telemetry.Debugf("[EXEC] skipping %s %s — limitPct %.1f → price <1¢", intent.Ticker, intent.Side, intent.LimitPct)
			continue
		}
		if priceCents > 99 {
			priceCents = 99
		}

		s.orderSeq++
		clientID := s.sessionID + ":" + strconv.FormatInt(s.orderSeq, 36)
		req := kalshi_http.CreateOrderRequest{
			Ticker:      intent.Ticker,
			Action:      "buy",
			Side:        intent.Side,
			Type:        "limit",
			CountFP:     "1.00",
			ClientID:    clientID,
			TimeInForce: "good_till_canceled",
		}
		if !intent.Slam {
			req.ExpirationTS = time.Now().Add(time.Duration(ttlSec) * time.Second).Unix()
		}
		priceDollars := fmt.Sprintf("%.2f", priceCents/100.0)
		if intent.Side == "yes" {
			req.YesPriceDollars = priceDollars
		} else {
			req.NoPriceDollars = priceDollars
		}
		reqs = append(reqs, req)
		kept = append(kept, intent)
	}
	intents = kept

	if len(reqs) == 0 {
		telemetry.Debugf("[EXEC] all orders skipped — no viable prices")
		return
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
		priceDollars := reqs[i].YesPriceDollars
		if intent.Side == "no" {
			priceDollars = reqs[i].NoPriceDollars
		}
		cents := dollarsToCents(priceDollars)
		label := "[ORDER]"
		if intent.Slam {
			label = "[SLAM]"
		}
		fmt.Fprintf(&ob, "%s%s %-*s  %-3s  1 contracts @ %d¢\n",
			prefix, label, nameWidth, teamFor(intent.Outcome),
			strings.ToUpper(intent.Side), cents)
	}
	fmt.Fprint(os.Stderr, ob.String())

	resp, err := s.client.PlaceBatchOrders(context.Background(), kalshi_http.BatchCreateOrdersRequest{
		Orders: reqs,
	})
	if err != nil {
		telemetry.Errorf("[RESPONSE] batch FAILED: %v", err)
		return
	}

	// ── RESPONSE block ──
	ts = time.Now().Format("3:04:05.000 PM")
	tsPrefix = fmt.Sprintf("[%s] ", ts)
	pad = strings.Repeat(" ", len(tsPrefix))

	if len(resp.Orders) == 0 {
		fmt.Fprintf(os.Stderr, "%s[RESPONSE] empty — Kalshi returned 0 order results\n", tsPrefix)
		return
	}

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
			fmt.Fprintf(&rb, "%s[RESPONSE] %-*s  %-3s  (nil order, nil error)\n",
				prefix, nameWidth, name, side)
			continue
		}

		o := r.Order
		total := o.FillCount + o.RemainingCount
		fillCost := o.TakerFillCost + o.MakerFillCost
		fees := o.TakerFees + o.MakerFees

		avg := float64(fillCost+fees) / max(float64(o.FillCount), 1)
		fmt.Fprintf(&rb, "%s[RESPONSE] %-*s  %-3s  [%d/%d] @ %.2f¢ avg\n",
			prefix, nameWidth, name, side, o.FillCount, total, avg)

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

	if s.tracker != nil && gcOK {
		s.tracker.RecordBatch(gc, intents, resp.Orders, ttlSec)
	}
}

func shortName(name string) string {
	i := strings.LastIndexByte(name, ' ')
	if i >= 0 {
		return name[i+1:]
	}
	return name
}

func dollarsToCents(s string) int {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(v * 100)
}
