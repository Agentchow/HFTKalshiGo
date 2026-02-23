package strategy

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
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

// Engine subscribes to ScoreChangeEvent, MarketEvent, and GameFinishEvent.
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
	backfillDelay  time.Duration
}

// SetSoccerTraining enables soccer training DB logging.
func (e *Engine) SetSoccerTraining(s *training.Store, backfillDelaySec int) {
	e.soccerTraining = s
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

	bus.Subscribe(events.EventScoreChange, e.onScoreChange)
	bus.Subscribe(events.EventGameFinish, e.onGameFinish)
	bus.Subscribe(events.EventMarketData, e.onMarketData)
	bus.Subscribe(events.EventWSStatus, e.onWSStatus)

	return e
}

func (e *Engine) onScoreChange(evt events.Event) error {
	sc, ok := evt.Payload.(events.ScoreChangeEvent)
	if !ok {
		return nil
	}

	gc, exists := e.store.Get(sc.Sport, sc.EID)
	if !exists {
		gameStart := evt.Timestamp
		if sc.GameStartUTC > 0 {
			gameStart = time.Unix(sc.GameStartUTC, 0)
		}
		gc = e.createGameContext(sc, gameStart)
		e.store.Put(gc)
		telemetry.Metrics.ActiveGames.Inc()
	}

	gc.Send(func() {
		strat, ok := e.registry.Get(sc.Sport)
		if !ok {
			return
		}

		prevHome := gc.Game.GetHomeScore()
		prevAway := gc.Game.GetAwayScore()
		prevHasLive := gc.Game.HasLiveData()

		result := strat.Evaluate(gc, &sc)
		e.publishIntents(result.Intents, sc.Sport, sc.League, sc.EID, evt.Timestamp)

		scoreChanged := gc.Game.GetHomeScore() != prevHome || gc.Game.GetAwayScore() != prevAway
		firstLive := !prevHasLive && gc.Game.HasLiveData()

		e.logSoccerTraining(gc, firstLive, scoreChanged)

		if len(gc.Tickers) == 0 {
			return
		}

		gc.Game.RecalcEdge(gc.Tickers)

		ds := e.display.Get(gc.EID)

		if !ds.DisplayedLive && (firstLive || scoreChanged) {
			ds.DisplayedLive = true
			if isGameStart(gc) {
				printGame(gc, "GAME-START")
			} else {
				printGame(gc, "LIVE")
			}
		} else if scoreChanged {
			printGame(gc, "GOAL")
		}

		for _, evt := range result.DisplayEvents {
			if ds.DisplayedLive {
				printGame(gc, evt)
			}
		}
	})

	return nil
}

// onGameFinish handles the game-over event.
//
// NOTE: Kalshi does NOT remove liquidity or settle markets immediately
// after a game ends. They wait several minutes and let the market
// participants handle it organically. Ticker prices continue to update
// via the WS feed after FINAL and should still be tracked.
func (e *Engine) onGameFinish(evt events.Event) error {
	gf, ok := evt.Payload.(events.GameFinishEvent)
	if !ok {
		return nil
	}

	gc, exists := e.store.Get(gf.Sport, gf.EID)
	if !exists {
		return nil
	}

	gc.Send(func() {
		ds := e.display.Get(gc.EID)
		if ds.Finaled {
			return
		}
		ds.Finaled = true

		strat, ok := e.registry.Get(gf.Sport)
		if !ok {
			return
		}

		intents := strat.OnFinish(gc, &gf)
		e.publishIntents(intents, gf.Sport, gf.League, gf.EID, evt.Timestamp)

		e.logSoccerTrainingFinish(gc, &gf)

		if ds.DisplayedLive {
			printGame(gc, "FINAL")
		}

		telemetry.Metrics.ActiveGames.Dec()
	})

	return nil
}

const edgeDisplayThrottle = 30 * time.Second

// onMarketData routes Kalshi WS price updates to the correct game.
//
// IMPORTANT: Kalshi markets are NOT binary complements. Each outcome
// (home, away, tie) has its own independent YES and NO order book with
// real people posting offers. Prices are NOT derivable from each other
// (e.g. NO ask != 100 - YES bid). Never synthesize or derive prices.
//
// The Kalshi WS sends PARTIAL ticker updates — only fields that changed
// are included. The parser uses -1 as a sentinel for "field not present",
// so we can distinguish absent fields (keep existing) from genuinely
// $0.00 (update to 0). Guards use >= 0 to accept zero-priced updates.
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
			if me.NoBid >= 0 {
				td.NoBid = me.NoBid
				td.YesAsk = 100 - me.NoBid
			}
			if me.Volume > 0 {
				td.Volume = me.Volume
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
		telemetry.Infof("strategy: Kalshi WS reconnected, waiting for live prices")
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

func (e *Engine) createGameContext(sc events.ScoreChangeEvent, gameStartedAt time.Time) *game.GameContext {
	gs := e.registry.CreateGameState(sc.Sport, sc.EID, sc.League, sc.HomeTeam, sc.AwayTeam)
	gc := game.NewGameContext(sc.Sport, sc.League, sc.EID, gs)
	gc.GameStartedAt = gameStartedAt

	if e.resolver != nil {
		go e.resolveTickers(gc, sc, gameStartedAt)
	}

	return gc
}

func (e *Engine) publishIntents(intents []events.OrderIntent, sport events.Sport, league, gameID string, ts time.Time) {
	for _, intent := range intents {
		telemetry.Metrics.OrderIntents.Inc()
		e.bus.Publish(events.Event{
			ID:        intent.Ticker,
			Type:      events.EventOrderIntent,
			Sport:     sport,
			League:    league,
			GameID:    gameID,
			Timestamp: ts,
			Payload:   intent,
		})
	}
}

// isGameStart returns true only when the game genuinely appears to be
// just starting: first period, no goals, and (if we have a real start
// time) within 5 minutes of puck-drop. AHL/KHL feeds often omit
// GameStartUTC, so we also look at game state to avoid labelling a
// 3-0 mid-game as "GAME-START".
func isGameStart(gc *game.GameContext) bool {
	if gc.Game.GetHomeScore() > 0 || gc.Game.GetAwayScore() > 0 {
		return false
	}
	p := strings.ToLower(gc.Game.GetPeriod())
	if p != "" && !strings.Contains(p, "1st") && p != "not started" {
		return false
	}
	if !gc.GameStartedAt.IsZero() && time.Since(gc.GameStartedAt) > 5*time.Minute {
		return false
	}
	return true
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

// ── Soccer training DB helpers ───────────────────────────────────────

func (e *Engine) logSoccerTraining(gc *game.GameContext, firstLive, scoreChanged bool) {
	if e.soccerTraining == nil || gc.Sport != events.SportSoccer {
		return
	}
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return
	}

	var eventType string
	if firstLive && isGameStart(gc) {
		eventType = "Game Start"
	} else if scoreChanged {
		eventType = "Score Change"
	} else {
		return
	}

	row := e.buildSoccerRow(gc, ss, eventType, nil)
	rowID, err := e.soccerTraining.Insert(row)
	if err != nil {
		telemetry.Warnf("soccer training: insert failed: %v", err)
		return
	}

	e.spawnBackfill(gc, ss, rowID)
}

func (e *Engine) logSoccerTrainingFinish(gc *game.GameContext, gf *events.GameFinishEvent) {
	if e.soccerTraining == nil || gc.Sport != events.SportSoccer {
		return
	}
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return
	}

	outcome := regulationOutcome(ss)
	row := e.buildSoccerRow(gc, ss, "Game Finish", &outcome)
	rowID, err := e.soccerTraining.Insert(row)
	if err != nil {
		telemetry.Warnf("soccer training: insert (finish) failed: %v", err)
		return
	}

	e.spawnBackfill(gc, ss, rowID)
}

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
		row.PregameHomePct = f64Ptr(ss.HomeWinPct)
		row.PregameDrawPct = f64Ptr(ss.DrawPct)
		row.PregameAwayPct = f64Ptr(ss.AwayWinPct)
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

			if ss.PinnacleHomePct != nil {
				v := *ss.PinnacleHomePct / 100.0
				odds.PinnacleHomePctL = &v
			}
			if ss.PinnacleDrawPct != nil {
				v := *ss.PinnacleDrawPct / 100.0
				odds.PinnacleDrawPctL = &v
			}
			if ss.PinnacleAwayPct != nil {
				v := *ss.PinnacleAwayPct / 100.0
				odds.PinnacleAwayPctL = &v
			}

			if gc.KalshiConnected && len(gc.Tickers) > 0 {
				if td, ok := gc.Tickers[ss.HomeTicker]; ok && td.YesAsk >= 0 {
					v := td.YesAsk / 100.0
					odds.KalshiHomePctL = &v
				}
				if td, ok := gc.Tickers[ss.DrawTicker]; ok && td.YesAsk >= 0 {
					v := td.YesAsk / 100.0
					odds.KalshiDrawPctL = &v
				}
				if td, ok := gc.Tickers[ss.AwayTicker]; ok && td.YesAsk >= 0 {
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

// resolveTickers runs async (HTTP call), then sends results back to the game goroutine.
func (e *Engine) resolveTickers(gc *game.GameContext, sc events.ScoreChangeEvent, gameStartedAt time.Time) {
	resolved := e.resolver.Resolve(context.Background(), sc.Sport, sc.HomeTeam, sc.AwayTeam, gameStartedAt)
	if resolved == nil {
		telemetry.Debugf("ticker: no match for %s %s vs %s", sc.Sport, sc.HomeTeam, sc.AwayTeam)
		return
	}

	allTickers := resolved.AllTickers()
	if e.subscriber != nil {
		if err := e.subscriber.SubscribeTickers(allTickers); err != nil {
			telemetry.Warnf("ticker: WS subscribe failed for %s vs %s: %v", sc.HomeTeam, sc.AwayTeam, err)
		}
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

		gc.Game.RecalcEdge(gc.Tickers)

		ds := e.display.Get(gc.EID)
		if !ds.DisplayedLive && gc.Game.HasLiveData() {
			ds.DisplayedLive = true
			if isGameStart(gc) {
				printGame(gc, "GAME-START")
			} else {
				printGame(gc, "LIVE")
			}
		}
	})
}
