package strategy

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/core/training"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// TickerSubscriber subscribes to Kalshi WS orderbook updates for specific
// market tickers. The strategy engine calls this when new tickers are
// discovered via the resolver.
type TickerSubscriber interface {
	SubscribeTickers(tickers []string) error
}

// Engine subscribes to GameUpdateEvent, MarketEvent, and WSStatusEvent.
// It routes each event to the correct GameContext's goroutine via Send().
// Strategy evaluation and order intent publishing happen on the game's
// goroutine — never on the webhook or WS goroutine.
type Engine struct {
	bus        *events.Bus
	store      *store.GameStateStore
	registry   *Registry
	resolver   *ticker.Resolver
	display    *display.Tracker
	subscriber TickerSubscriber

	kalshiWSUp atomic.Bool

	soccerTraining *training.Store
	hockeyTraining *training.HockeyStore
	backfillDelay  time.Duration
}

// SetSoccerTraining enables soccer training DB logging.
func (e *Engine) SetSoccerTraining(s *training.Store, backfillDelaySec int) {
	e.soccerTraining = s
	e.backfillDelay = time.Duration(backfillDelaySec) * time.Second
}

// SetHockeyTraining enables hockey training DB logging.
func (e *Engine) SetHockeyTraining(s *training.HockeyStore, backfillDelaySec int) {
	e.hockeyTraining = s
	e.backfillDelay = time.Duration(backfillDelaySec) * time.Second
}

func NewEngine(bus *events.Bus, gameStore *store.GameStateStore, registry *Registry, resolver *ticker.Resolver, subscriber TickerSubscriber) *Engine {
	e := &Engine{
		bus:        bus,
		store:      gameStore,
		registry:   registry,
		resolver:   resolver,
		subscriber: subscriber,
		display:    display.NewTracker(),
	}
	e.kalshiWSUp.Store(true)

	bus.Subscribe(events.EventGameUpdate, e.onGameUpdate)
	bus.Subscribe(events.EventMarketData, e.onMarketData)
	bus.Subscribe(events.EventWSStatus, e.onWSStatus)

	return e
}

func (e *Engine) onGameUpdate(evt events.Event) error {
	gu, ok := evt.Payload.(events.GameUpdateEvent)
	if !ok {
		return nil
	}

	gc, exists := e.store.Get(gu.Sport, gu.EID)
	if !exists {
		gameStart := evt.Timestamp
		if gu.GameStartUTC > 0 {
			gameStart = time.Unix(gu.GameStartUTC, 0)
		}
		gc = e.createGameContext(gu, gameStart)
		e.store.Put(gc)
		telemetry.Metrics.ActiveGames.Inc()
	}

	gc.Send(func() {
		// ── Finish path ─────────────────────────────────────────
		if isFinished(gu.Period) {
			ds := e.display.Get(gc.EID)
			if ds.Finaled {
				return
			}
			ds.Finaled = true
			telemetry.Metrics.ActiveGames.Dec()

			if len(gc.Tickers) == 0 {
				return
			}

			strat, ok := e.registry.Get(gu.Sport)
			if !ok {
				return
			}

			intents := strat.OnFinish(gc, &gu)
			e.publishIntents(intents, gu.Sport, gu.League, gu.EID, evt.Timestamp)

			defer gc.SetMatchStatus("Game Finish")
			return
		}

		// ── Live path ───────────────────────────────────────────
		strat, ok := e.registry.Get(gu.Sport)
		if !ok {
			return
		}

		prevHome := gc.Game.GetHomeScore()
		prevAway := gc.Game.GetAwayScore()

		result := strat.Evaluate(gc, &gu)

		if len(gc.Tickers) == 0 {
			return
		}

		e.publishIntents(result.Intents, gu.Sport, gu.League, gu.EID, evt.Timestamp)

		// ── MatchStatus ─────────────────────────────────────────
		scoreChanged := gc.Game.GetHomeScore() != prevHome || gc.Game.GetAwayScore() != prevAway
		status := gu.MatchStatus // parser-derived: "Game Start", "Overtime", or "Live"
		if scoreChanged {
			status = "Score Change"
		} else if status == "Game Start" {
			ds := e.display.Get(gc.EID)
			if ds.GameStarted {
				status = "Live"
			} else {
				ds.GameStarted = true
			}
		} else {
			status = gc.Game.DeduplicateStatus(status)
		}

		if !gc.Game.HasPregame() {
			ds := e.display.Get(gc.EID)
			if time.Since(ds.LastPregameWarn) >= 10*time.Second {
				telemetry.Warnf("game %s (%s vs %s): suppressed — pregame odds not yet loaded",
					gc.EID, gc.Game.GetHomeTeam(), gc.Game.GetAwayTeam())
				ds.LastPregameWarn = time.Now()
			}
			return
		}

		gc.Game.RecalcEdge(gc.Tickers)

		if status == "Live" {
			ds := e.display.Get(gc.EID)
			if !ds.DisplayedLive {
				ds.DisplayedLive = true
				gc.SetMatchStatus("Live")
			}
		} else {
			ds := e.display.Get(gc.EID)
			if !ds.DisplayedLive {
				ds.DisplayedLive = true
			}
			gc.SetMatchStatus(status)
		}
	})

	return nil
}

func isFinished(period string) bool {
	low := strings.ToLower(strings.TrimSpace(period))
	for _, tok := range []string{
		"finished", "final", "ended", "ft",
		"after over time", "after overtime", "after ot",
		"after extra time", "aet",
		"after penalties", "after pen",
	} {
		if low == tok || strings.Contains(low, tok) {
			return true
		}
	}
	return false
}

const edgeDisplayThrottle = 30 * time.Second

// onMarketData routes Kalshi WS price updates to the correct game.
//
// The Kalshi WS ticker channel sends yes_bid_dollars and yes_ask_dollars.
// NO prices are the binary complement: no_ask = 100 - yes_bid,
// no_bid = 100 - yes_ask (same orders, opposite side of the book).
//
// The WS sends PARTIAL updates — only fields that changed are included.
// The parser uses -1 as a sentinel for "field not present", so we can
// distinguish absent fields (keep existing) from genuinely $0.00.
// Guards use >= 0 to accept zero-priced updates.
func (e *Engine) onMarketData(evt events.Event) error {
	me, ok := evt.Payload.(events.MarketEvent)
	if !ok {
		return nil
	}

	targets := e.store.ByTicker(me.Ticker)
	if len(targets) == 0 {
		return nil
	}

	for _, gc := range targets {
		gc.Send(func() {
			gc.KalshiConnected = true
			td := gc.Tickers[me.Ticker]
			if td == nil {
				td = &game.TickerData{Ticker: me.Ticker, NoAsk: 100, NoBid: 100}
				gc.Tickers[me.Ticker] = td
			}
			if me.YesBid >= 0 {
				td.YesBid = me.YesBid
				td.NoAsk = 100 - me.YesBid
			}
			if me.YesAsk >= 0 {
				td.YesAsk = me.YesAsk
				td.NoBid = 100 - me.YesAsk
			}
			if me.Volume > 0 {
				td.Volume = me.Volume
			}

			if !gc.Game.HasPregame() {
				return
			}

			gc.Game.RecalcEdge(gc.Tickers)

			ds := e.display.Get(gc.EID)

			if !gc.Game.HasLiveData() || !ds.DisplayedLive {
				return
			}

			strat, ok := e.registry.Get(gc.Sport)
			if !ok {
				return
			}
			intents := strat.OnPriceUpdate(gc)
			if len(intents) > 0 {
				e.publishIntents(intents, gc.Sport, gc.League, gc.EID, evt.Timestamp)
			}

			if time.Since(ds.LastEdgeDisplay) < edgeDisplayThrottle {
				return
			}
			if strat.HasSignificantEdge(gc) {
				ds.LastEdgeDisplay = time.Now()
				printGame(gc, "EDGE")
			}
		})
	}
	return nil
}

func (e *Engine) onWSStatus(evt events.Event) error {
	ws, ok := evt.Payload.(events.WSStatusEvent)
	if !ok {
		return nil
	}

	connected := ws.Connected
	e.kalshiWSUp.Store(connected)

	if connected {
		telemetry.Infof("strategy: Kalshi WS connected, waiting for live prices")
	} else {
		telemetry.Warnf("strategy: Kalshi WS disconnected, resetting all ticker prices to 100")
	}

	for _, gc := range e.store.All() {
		gc.Send(func() {
			gc.KalshiConnected = connected
			if !connected {
				for _, td := range gc.Tickers {
					td.YesAsk = 100
					td.YesBid = 100
					td.NoAsk = 100
					td.NoBid = 100
				}
			}
		})
	}
	return nil
}

func (e *Engine) createGameContext(gu events.GameUpdateEvent, gameStartedAt time.Time) *game.GameContext {
	gs := e.registry.CreateGameState(gu.Sport, gu.EID, gu.League, gu.HomeTeam, gu.AwayTeam)
	gc := game.NewGameContext(gu.Sport, gu.League, gu.EID, gs)
	gc.GameStartedAt = gameStartedAt

	gc.OnMatchStatusChange = e.onMatchStatusChange
	gc.OnRedCardChange = e.onRedCardChange
	gc.OnPowerPlayChange = e.onPowerPlayChange

	if e.resolver != nil {
		go e.resolveTickers(gc, gu, gameStartedAt)
	}

	return gc
}

func (e *Engine) publishIntents(intents []events.OrderIntent, sport events.Sport, league, gameID string, ts time.Time) {
	if len(intents) == 0 {
		return
	}
	telemetry.Metrics.OrderIntents.Add(int64(len(intents)))
	e.bus.Publish(events.Event{
		ID:        gameID,
		Type:      events.EventOrderIntent,
		Sport:     sport,
		League:    league,
		GameID:    gameID,
		Timestamp: ts,
		Payload:   intents,
	})
}

func printGame(gc *game.GameContext, eventType string) {
	switch gc.Sport {
	case events.SportHockey:
		display.PrintHockey(gc, eventType)
	case events.SportSoccer:
		display.PrintSoccer(gc, eventType)
	case events.SportFootball:
		display.PrintFootball(gc, eventType)
	}
}

// onMatchStatusChange is the callback wired to gc.OnMatchStatusChange.
// Prints the event and writes a training snapshot.
func (e *Engine) onMatchStatusChange(gc *game.GameContext) {
	status := gc.MatchStatus

	ds := e.display.Get(gc.EID)
	if !ds.DisplayedLive {
		ds.DisplayedLive = true
	}
	printGame(gc, status)

	switch gc.Sport {
	case events.SportSoccer:
		if e.soccerTraining == nil || isMockGame(gc.EID) {
			return
		}
		ss, ok := gc.Game.(*soccerState.SoccerState)
		if !ok {
			return
		}
		if status == "Score Change" && !ss.PinnacleUpdated {
			return
		}
		var outcome *string
		if status == "Game Finish" {
			o := regulationOutcome(ss)
			outcome = &o
		}
		row := e.buildSoccerRow(gc, ss, status, outcome)
		rowID, err := e.soccerTraining.Insert(row)
		if err != nil {
			telemetry.Warnf("soccer training: insert failed: %v", err)
			return
		}
		e.spawnBackfill(gc, ss, rowID)

	case events.SportHockey:
		if e.hockeyTraining == nil || isMockGame(gc.EID) {
			return
		}
		hs, ok := gc.Game.(*hockeyState.HockeyState)
		if !ok {
			return
		}
		if status == "Score Change" && !hs.PinnacleUpdated {
			return
		}
		var outcome *string
		if status == "Game Finish" {
			o := hockeyOutcome(hs.HomeScore, hs.AwayScore)
			outcome = &o
		}
		row := e.buildHockeyRow(gc, hs, status, outcome)
		rowID, err := e.hockeyTraining.Insert(row)
		if err != nil {
			telemetry.Warnf("hockey training: insert failed: %v", err)
			return
		}
		e.spawnHockeyBackfill(gc, hs, rowID)
	}
}

// onRedCardChange is fired when soccer red card counts change.
func (e *Engine) onRedCardChange(gc *game.GameContext, homeRC, awayRC int) {
	printGame(gc, "Red Card")

	if e.soccerTraining == nil || isMockGame(gc.EID) {
		return
	}
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return
	}
	row := e.buildSoccerRow(gc, ss, "Red Card", nil)
	rowID, err := e.soccerTraining.Insert(row)
	if err != nil {
		telemetry.Warnf("soccer training: insert failed: %v", err)
		return
	}
	e.spawnBackfill(gc, ss, rowID)
}

// onPowerPlayChange is fired when hockey power play state transitions.
func (e *Engine) onPowerPlayChange(gc *game.GameContext, homeOn, awayOn bool) {
	label := "Power Play End"
	if homeOn || awayOn {
		label = "Power Play"
	}
	printGame(gc, label)

	if e.hockeyTraining == nil || isMockGame(gc.EID) {
		return
	}
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return
	}
	row := e.buildHockeyRow(gc, hs, label, nil)
	rowID, err := e.hockeyTraining.Insert(row)
	if err != nil {
		telemetry.Warnf("hockey training: insert failed: %v", err)
		return
	}
	e.spawnHockeyBackfill(gc, hs, rowID)
}

// ── Soccer training DB helpers ───────────────────────────────────────

func (e *Engine) buildSoccerRow(gc *game.GameContext, ss *soccerState.SoccerState, eventType string, outcome *string) training.SoccerRow {
	row := training.SoccerRow{
		Ts:            time.Now(),
		GameID:        gc.EID,
		League:        gc.League,
		HomeTeam:      ss.HomeTeam,
		AwayTeam:      ss.AwayTeam,
		NormHome:      ticker.Normalize(ss.HomeTeam, ticker.SoccerAliases),
		NormAway:      ticker.Normalize(ss.AwayTeam, ticker.SoccerAliases),
		Half:          ss.Half,
		EventType:     eventType,
		HomeScore:     ss.HomeScore,
		AwayScore:     ss.AwayScore,
		TimeRemain:    ss.TimeLeft,
		RedCardsHome:  ss.HomeRedCards,
		RedCardsAway:  ss.AwayRedCards,
		ActualOutcome: outcome,
	}

	if ss.PregameApplied {
		row.PregameHomePct = f64Ptr(ss.HomeStrength)
		row.PregameDrawPct = f64Ptr(ss.DrawPct)
		row.PregameAwayPct = f64Ptr(ss.AwayStrength)
		row.PregameG0 = f64Ptr(ss.G0)
	}

	return row
}

func (e *Engine) spawnBackfill(gc *game.GameContext, ss *soccerState.SoccerState, rowID int64) {
	delay := e.backfillDelay
	go func() {
		time.Sleep(delay)
		gc.Send(func() {
			odds := training.OddsBackfill{}

			if ss.PinnacleHomePct != nil && *ss.PinnacleHomePct > 0 {
				v := *ss.PinnacleHomePct / 100.0
				odds.PinnacleHomePctL = &v
			}
			if ss.PinnacleDrawPct != nil && *ss.PinnacleDrawPct > 0 {
				v := *ss.PinnacleDrawPct / 100.0
				odds.PinnacleDrawPctL = &v
			}
			if ss.PinnacleAwayPct != nil && *ss.PinnacleAwayPct > 0 {
				v := *ss.PinnacleAwayPct / 100.0
				odds.PinnacleAwayPctL = &v
			}

			if len(gc.Tickers) > 0 {
				if td, ok := gc.Tickers[ss.HomeTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiHomePctL = &v
				}
				if td, ok := gc.Tickers[ss.DrawTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiDrawPctL = &v
				}
				if td, ok := gc.Tickers[ss.AwayTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiAwayPctL = &v
				}
			}

			e.soccerTraining.BackfillOdds(rowID, odds)
		})
	}()
}

// regulationOutcome returns the 1X2 result based on the regulation-time
// score, which is what Kalshi settles on. SoccerState freezes the
// regulation score when the game transitions to extra time or penalties.
func regulationOutcome(ss *soccerState.SoccerState) string {
	diff := ss.RegulationGoalDiff()
	if diff > 0 {
		return "home_win"
	}
	if diff < 0 {
		return "away_win"
	}
	return "draw"
}

func f64Ptr(v float64) *float64 { return &v }

func isMockGame(eid string) bool { return strings.HasPrefix(eid, "MOCK-") }

// ── Hockey training DB helpers ───────────────────────────────────────

func (e *Engine) buildHockeyRow(gc *game.GameContext, hs *hockeyState.HockeyState, eventType string, outcome *string) training.HockeyRow {
	row := training.HockeyRow{
		Ts:            time.Now(),
		GameID:        gc.EID,
		League:        gc.League,
		HomeTeam:      hs.HomeTeam,
		AwayTeam:      hs.AwayTeam,
		NormHome:      ticker.Normalize(hs.HomeTeam, ticker.HockeyAliases),
		NormAway:      ticker.Normalize(hs.AwayTeam, ticker.HockeyAliases),
		EventType:     eventType,
		HomeScore:     hs.HomeScore,
		AwayScore:     hs.AwayScore,
		Period:        hs.Period,
		TimeRemain:    hs.TimeLeft,
		HomePowerPlay: hs.IsHomePowerPlay,
		AwayPowerPlay: hs.IsAwayPowerPlay,
		ActualOutcome: outcome,
	}

	if hs.PregameApplied {
		row.PregameHomePct = f64Ptr(hs.HomeStrength)
		row.PregameAwayPct = f64Ptr(hs.AwayStrength)
		row.PregameG0 = hs.PregameG0
	}

	return row
}

func (e *Engine) spawnHockeyBackfill(gc *game.GameContext, hs *hockeyState.HockeyState, rowID int64) {
	delay := e.backfillDelay
	go func() {
		time.Sleep(delay)
		gc.Send(func() {
			odds := training.HockeyOddsBackfill{}

			if hs.PinnacleHomePct != nil && *hs.PinnacleHomePct > 0 {
				v := *hs.PinnacleHomePct / 100.0
				odds.PinnacleHomePctL = &v
			}
			if hs.PinnacleAwayPct != nil && *hs.PinnacleAwayPct > 0 {
				v := *hs.PinnacleAwayPct / 100.0
				odds.PinnacleAwayPctL = &v
			}

			if len(gc.Tickers) > 0 {
				if td, ok := gc.Tickers[hs.HomeTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiHomePctL = &v
				}
				if td, ok := gc.Tickers[hs.AwayTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiAwayPctL = &v
				}
			}

			e.hockeyTraining.BackfillOdds(rowID, odds)
		})
	}()
}

// hockeyOutcome returns the outcome for a finished hockey game.
// Tied scores at finish mean the game went to a shootout that
// GoalServe didn't report individual goals for.
func hockeyOutcome(homeScore, awayScore int) string {
	if homeScore > awayScore {
		return "home_win"
	}
	if awayScore > homeScore {
		return "away_win"
	}
	return "shootout"
}

// resolveTickers runs async (HTTP call), then sends results back to the game goroutine.
func (e *Engine) resolveTickers(gc *game.GameContext, gu events.GameUpdateEvent, gameStartedAt time.Time) {
	resolved := e.resolver.Resolve(context.Background(), gu.Sport, gu.HomeTeam, gu.AwayTeam, gameStartedAt)
	if resolved == nil {
		telemetry.Debugf("ticker: no match for %s %s vs %s", gu.Sport, gu.HomeTeam, gu.AwayTeam)
		return
	}

	allTickers := resolved.AllTickers()
	if e.subscriber != nil {
		if err := e.subscriber.SubscribeTickers(allTickers); err != nil {
			telemetry.Warnf("ticker: WS subscribe failed for %s vs %s: %v", gu.HomeTeam, gu.AwayTeam, err)
		}
	}

	initialStatus := gu.MatchStatus
	if initialStatus == "" {
		initialStatus = "Live"
	}

	gc.Send(func() {
		gc.KalshiConnected = e.kalshiWSUp.Load()
		gc.Game.SetTickers(resolved.HomeTicker, resolved.AwayTicker, resolved.DrawTicker)
		gc.KalshiEventURL = ticker.KalshiEventURL(resolved.EventTicker)

		for _, t := range allTickers {
			td := &game.TickerData{Ticker: t, NoAsk: 100, NoBid: 100}
			if snap, ok := resolved.Prices[t]; ok {
				td.YesAsk = float64(snap.YesAsk)
				td.YesBid = float64(snap.YesBid)
				td.NoAsk = float64(snap.NoAsk)
				td.NoBid = float64(snap.NoBid)
				td.Volume = snap.Volume
			}
			gc.Tickers[t] = td
			e.store.RegisterTicker(t, gc)
		}

		ds := e.display.Get(gc.EID)
		if initialStatus == "Game Start" {
			ds.GameStarted = true
		}
		gc.SetMatchStatus(initialStatus)

		if gc.Game.HasPregame() {
			gc.Game.RecalcEdge(gc.Tickers)
		}
	})
}
