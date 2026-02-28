package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// OrderPoller is satisfied by *kalshi_http.Client and lets the tracker
// poll final fill status without importing the HTTP package at the call site.
type OrderPoller interface {
	GetOrder(ctx context.Context, orderID string) (*kalshi_http.OrderDetail, error)
	ReadTokens() float64
}

// BatchOrderContext captures the full lifecycle of a single batch order.
type BatchOrderContext struct {
	ID        int64
	GameEID   string
	Sport     string
	League    string
	HomeTeam  string
	AwayTeam  string
	OrderType   string // "regular" or "slam"
	OrderTTLSec int
	PlacedAt    time.Time

	HomeScore int
	AwayScore int
	Period    string
	TimeLeft  string

	Home *OutcomeOrder // nil if no order on home ticker
	Away *OutcomeOrder
	Draw *OutcomeOrder // soccer only

	Prices1s  *PriceSnapshot
	Prices5s  *PriceSnapshot
	Prices10s *PriceSnapshot

	FinalOutcome string // "home", "away", "draw"
	FinalPnL     int    // cents
}

// OutcomeOrder records details for one outcome leg of a batch.
type OutcomeOrder struct {
	OrderID    string
	Ticker     string
	Side       string // "yes" or "no"
	LimitCents int
	CostCents  int // fill cost + fees combined
	FillCount  int
	TotalCount int // fill + remaining
}

// PriceSnapshot captures yes-ask for each outcome ticker at a point in time.
type PriceSnapshot struct {
	HomeYesAsk float64
	AwayYesAsk float64
	DrawYesAsk float64
}

// Tracker records batch orders, schedules follow-up price captures,
// and settles P&L on game finish. It implements game.GameObserver.
type Tracker struct {
	store  *Store
	poller OrderPoller
}

var _ game.GameObserver = (*Tracker)(nil)

func NewTracker(store *Store, poller OrderPoller) *Tracker {
	return &Tracker{store: store, poller: poller}
}

// RecordBatch builds a BatchOrderContext from the fill results and persists it.
// It spawns a goroutine to capture follow-up prices at 1s/5s/10s.
//
// Called from the execution service's placeBatchOrder goroutine (not the game goroutine).
func (t *Tracker) RecordBatch(
	gc *game.GameContext,
	intents []events.OrderIntent,
	responses []kalshi_http.BatchCreateOrdersIndividualResponse,
	ttlSec int,
) {
	if t == nil || t.store == nil || len(intents) == 0 {
		return
	}

	orderType := "regular"
	if intents[0].Slam {
		orderType = "slam"
	}

	boc := &BatchOrderContext{
		GameEID:     intents[0].EID,
		Sport:       string(intents[0].Sport),
		League:      intents[0].League,
		HomeTeam:    gc.Game.GetHomeTeam(),
		AwayTeam:    gc.Game.GetAwayTeam(),
		OrderType:   orderType,
		OrderTTLSec: ttlSec,
		PlacedAt:    time.Now(),
		HomeScore: intents[0].HomeScore,
		AwayScore: intents[0].AwayScore,
		Period:    gc.Game.GetPeriod(),
		TimeLeft:  formatTimeLeft(gc.Game.GetTimeRemaining()),
	}

	for i, intent := range intents {
		if i >= len(responses) {
			break
		}
		r := responses[i]
		if r.Error != nil || r.Order == nil {
			continue
		}

		o := r.Order
		oo := &OutcomeOrder{
			OrderID:    o.OrderID,
			Ticker:     intent.Ticker,
			Side:       intent.Side,
			LimitCents: int(math.Floor(intent.LimitPct)),
			CostCents:  o.TakerFillCost + o.MakerFillCost + o.TakerFees + o.MakerFees,
			FillCount:  o.FillCount,
			TotalCount: o.FillCount + o.RemainingCount,
		}

		switch intent.Outcome {
		case "home":
			boc.Home = oo
		case "away":
			boc.Away = oo
		case "draw":
			boc.Draw = oo
		}
	}

	if boc.Home == nil && boc.Away == nil && boc.Draw == nil {
		return
	}

	rowID, err := t.store.InsertBatch(boc)
	if err != nil {
		telemetry.Warnf("tracking: insert failed: %v", err)
		return
	}
	boc.ID = rowID

	telemetry.Infof("[TRACKING] batch #%d recorded for %s vs %s (eid=%s, type=%s)",
		rowID, boc.HomeTeam, boc.AwayTeam, boc.GameEID, boc.OrderType)

	go t.captureFollowUpPrices(gc, boc)
	go t.backfillFills(boc)
}

// captureFollowUpPrices reads ticker prices at T+1s, T+5s, T+10s via gc.Send.
func (t *Tracker) captureFollowUpPrices(gc *game.GameContext, boc *BatchOrderContext) {
	checkpoints := []struct {
		delay time.Duration
		label string
	}{
		{1 * time.Second, "1s"},
		{4 * time.Second, "5s"},  // 1 + 4 = 5s total
		{5 * time.Second, "10s"}, // 5 + 5 = 10s total
	}

	for _, cp := range checkpoints {
		time.Sleep(cp.delay)

		ch := make(chan PriceSnapshot, 1)
		gc.Send(func() {
			ch <- readPriceSnapshot(gc, boc)
		})

		select {
		case snap := <-ch:
			t.store.UpdateFollowUpPrices(boc.ID, cp.label, snap)
		case <-time.After(5 * time.Second):
			telemetry.Warnf("tracking: timeout reading %s prices for batch #%d", cp.label, boc.ID)
		}
	}
}

// readPriceSnapshot extracts yes_ask prices for each outcome ticker from the game context.
// Must be called from the game's goroutine (inside gc.Send).
func readPriceSnapshot(gc *game.GameContext, boc *BatchOrderContext) PriceSnapshot {
	snap := PriceSnapshot{}
	if boc.Home != nil {
		if td := gc.Tickers[boc.Home.Ticker]; td != nil {
			snap.HomeYesAsk = td.YesAsk
		}
	}
	if boc.Away != nil {
		if td := gc.Tickers[boc.Away.Ticker]; td != nil {
			snap.AwayYesAsk = td.YesAsk
		}
	}
	if boc.Draw != nil {
		if td := gc.Tickers[boc.Draw.Ticker]; td != nil {
			snap.DrawYesAsk = td.YesAsk
		}
	}
	return snap
}

// backfillFills waits for orders to expire (TTL + 5s buffer), then polls
// Kalshi for final fill data once the read rate-limit bucket has > 8 tokens
// (waiting up to 30s for budget).
func (t *Tracker) backfillFills(boc *BatchOrderContext) {
	if t.poller == nil || boc.OrderType == "slam" {
		return
	}

	orderIDs := collectOrderIDs(boc)
	if len(orderIDs) == 0 {
		return
	}

	sleepSec := boc.OrderTTLSec + 5
	if sleepSec < 10 {
		sleepSec = 10
	}
	time.Sleep(time.Duration(sleepSec) * time.Second)

	if !t.waitForBudget(boc.ID) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for outcome, orderID := range orderIDs {
		detail, err := t.poller.GetOrder(ctx, orderID)
		if err != nil {
			telemetry.Warnf("tracking: backfill GetOrder(%s) for batch #%d: %v", orderID, boc.ID, err)
			continue
		}

		finalCost := detail.TakerFillCost + detail.MakerFillCost + detail.TakerFees + detail.MakerFees
		t.store.UpdateFinalFill(boc.ID, outcome, detail.OrderID, finalCost, detail.FillCount, detail.FillCount+detail.RemainingCount)
	}

	telemetry.Infof("[TRACKING] backfilled fills for batch #%d (%d orders polled)", boc.ID, len(orderIDs))
}

// waitForBudget polls ReadTokens every 2s for up to 30s, returning true
// once the bucket has > 8 tokens available.
func (t *Tracker) waitForBudget(batchID int64) bool {
	const maxWait = 30 * time.Second
	const poll = 2 * time.Second

	deadline := time.Now().Add(maxWait)
	for {
		if t.poller.ReadTokens() > 8 {
			return true
		}
		if time.Now().After(deadline) {
			telemetry.Debugf("tracking: skipping fill backfill for batch #%d (no budget after 30s)", batchID)
			return false
		}
		time.Sleep(poll)
	}
}

func collectOrderIDs(boc *BatchOrderContext) map[string]string {
	ids := make(map[string]string)
	if boc.Home != nil && boc.Home.OrderID != "" {
		ids["home"] = boc.Home.OrderID
	}
	if boc.Away != nil && boc.Away.OrderID != "" {
		ids["away"] = boc.Away.OrderID
	}
	if boc.Draw != nil && boc.Draw.OrderID != "" {
		ids["draw"] = boc.Draw.OrderID
	}
	return ids
}

// OnGameEvent implements game.GameObserver. On GAME_FINISH, it settles
// all unsettled batch orders for the game by computing realized P&L.
func (t *Tracker) OnGameEvent(gc *game.GameContext, eventType string) {
	if eventType != string(events.StatusGameFinish) {
		return
	}

	eid := gc.EID
	homeScore := gc.Game.GetHomeScore()
	awayScore := gc.Game.GetAwayScore()

	var outcome string
	switch {
	case homeScore > awayScore:
		outcome = "home"
	case awayScore > homeScore:
		outcome = "away"
	default:
		outcome = "draw"
	}

	rows, err := t.store.UnsettledForEID(eid)
	if err != nil {
		telemetry.Warnf("tracking: settle query for eid=%s: %v", eid, err)
		return
	}
	if len(rows) == 0 {
		return
	}

	for _, r := range rows {
		pnl := computePnL(r, outcome)
		t.store.UpdateSettlement(r.ID, outcome, pnl)
	}

	telemetry.Infof("[TRACKING] settled %d batch(es) for eid=%s outcome=%s", len(rows), eid, outcome)
}

// computePnL calculates realized P&L across all outcome legs of a batch.
// For each leg: if side aligns with the winning outcome, the contract settles
// at 100 cents, otherwise 0. P&L = settlement_value - cost.
func computePnL(r batchRow, outcome string) int {
	pnl := 0
	pnl += legPnL(r.HomeCostCents, r.HomeSide, "home", outcome)
	pnl += legPnL(r.AwayCostCents, r.AwaySide, "away", outcome)
	pnl += legPnL(r.DrawCostCents, r.DrawSide, "draw", outcome)
	return pnl
}

// legPnL computes P&L for one outcome leg.
//
// A YES buy on the winning outcome settles at 100. A NO buy on a losing
// outcome also settles at 100 (you bought NO on a team that lost = correct).
func legPnL(costCents sql.NullInt64, side sql.NullString, legOutcome, gameOutcome string) int {
	if !costCents.Valid || !side.Valid {
		return 0
	}
	cost := int(costCents.Int64)
	if cost == 0 {
		return 0
	}

	won := false
	switch side.String {
	case "yes":
		won = (legOutcome == gameOutcome)
	case "no":
		won = (legOutcome != gameOutcome)
	}

	if won {
		return 100 - cost
	}
	return -cost
}

func (t *Tracker) Close() error {
	if t == nil {
		return nil
	}
	return t.store.Close()
}

func formatTimeLeft(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	m := int(seconds) / 60
	s := int(seconds) % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
