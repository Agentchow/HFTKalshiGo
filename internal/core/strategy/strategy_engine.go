package strategy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/goalserve_http"
	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Engine subscribes to ScoreChangeEvent, MarketEvent, and GameFinishEvent.
// It routes each event to the correct GameContext's goroutine via Send().
// Strategy evaluation and order intent publishing happen on the game's
// goroutine — never on the webhook or WS goroutine.
type Engine struct {
	bus       *events.Bus
	store     *store.GameStateStore
	registry  *Registry
	resolver  *ticker.Resolver
	pregame   *goalserve_http.PregameClient
	startedAt time.Time

	pregameMu      sync.RWMutex
	pregameCache   []goalserve_http.PregameOdds
	pregameFetch   time.Time
	pregameLastTry time.Time
}

const pregameCacheTTL   = 30 * time.Minute
const pregameRetryCool  = 30 * time.Second

func NewEngine(bus *events.Bus, gameStore *store.GameStateStore, registry *Registry, resolver *ticker.Resolver, pregame *goalserve_http.PregameClient) *Engine {
	e := &Engine{
		bus:       bus,
		store:     gameStore,
		registry:  registry,
		resolver:  resolver,
		pregame:   pregame,
		startedAt: time.Now(),
	}

	bus.Subscribe(events.EventScoreChange, e.onScoreChange)
	bus.Subscribe(events.EventGameFinish, e.onGameFinish)
	bus.Subscribe(events.EventMarketData, e.onMarketData)

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

	// Send the work to the game's goroutine. This returns immediately —
	// the webhook goroutine is free to respond 200 to GoalServe.
	gc.Send(func() {
		pregameJustApplied := false
		if sc.Sport == events.SportSoccer && !gc.PregameApplied {
			if ss, ok := gc.Game.(*soccerState.SoccerState); ok {
				gc.PregameApplied = e.applySoccerPregame(ss, sc.HomeTeam, sc.AwayTeam)
				pregameJustApplied = gc.PregameApplied
			}
		}

		strat, ok := e.registry.Get(sc.Sport)
		if !ok {
			return
		}

		prevHome := gc.Game.GetHomeScore()
		prevAway := gc.Game.GetAwayScore()
		prevHasLive := gc.Game.HasLiveData()

		var prevHomeRC, prevAwayRC int
		if ss, ok := gc.Game.(*soccerState.SoccerState); ok {
			prevHomeRC = ss.HomeRedCards
			prevAwayRC = ss.AwayRedCards
		}

		intents := strat.Evaluate(gc, &sc)
		e.publishIntents(intents, sc.Sport, sc.League, sc.EID, evt.Timestamp)

		scoreChanged := gc.Game.GetHomeScore() != prevHome || gc.Game.GetAwayScore() != prevAway
		firstLive := !prevHasLive && gc.Game.HasLiveData()

		if len(gc.Tickers) == 0 {
			return
		}

		if !gc.DisplayedLive && (firstLive || scoreChanged) {
			gc.DisplayedLive = true
			if time.Since(e.startedAt) < 10*time.Second {
				printGame(gc, "LIVE")
			} else {
				printGame(gc, "GAME-START")
			}
		} else if scoreChanged {
			printGame(gc, "GOAL")
		} else if pregameJustApplied && gc.DisplayedLive {
			printGame(gc, "LIVE")
		}

		if ss, ok := gc.Game.(*soccerState.SoccerState); ok {
			if ss.HomeRedCards != prevHomeRC || ss.AwayRedCards != prevAwayRC {
				if gc.DisplayedLive {
					printGame(gc, "RED-CARD")
				}
			}
		}
	})

	return nil
}

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
		if gc.Finaled {
			return
		}
		gc.Finaled = true

		strat, ok := e.registry.Get(gf.Sport)
		if !ok {
			return
		}

		intents := strat.OnFinish(gc, &gf)
		e.publishIntents(intents, gf.Sport, gf.League, gf.EID, evt.Timestamp)

		if gc.DisplayedLive {
			printGame(gc, "FINAL")
		}

		telemetry.Metrics.ActiveGames.Dec()
	})

	return nil
}

const edgeDisplayThrottle = 30 * time.Second

// onMarketData routes Kalshi WS price updates to the correct game.
func (e *Engine) onMarketData(evt events.Event) error {
	me, ok := evt.Payload.(events.MarketEvent)
	if !ok {
		return nil
	}

	td := &game.TickerData{
		Ticker: me.Ticker,
		YesAsk: me.YesAsk,
		YesBid: me.YesBid,
		NoAsk:  me.NoAsk,
		NoBid:  me.NoBid,
		Volume: me.Volume,
	}

	for _, gc := range e.store.All() {
		gc.Send(func() {
			if _, exists := gc.Tickers[me.Ticker]; !exists {
				return
			}
			gc.UpdateTicker(td)

			if !gc.Game.HasLiveData() || !gc.DisplayedLive {
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

			if time.Since(gc.LastEdgeDisplay) < edgeDisplayThrottle {
				return
			}
			if hasSignificantEdge(gc) {
				gc.LastEdgeDisplay = time.Now()
				printGame(gc, "EDGE")
			}
		})
	}
	return nil
}

func (e *Engine) createGameContext(sc events.ScoreChangeEvent, gameStartedAt time.Time) *game.GameContext {
	gs := e.registry.CreateGameState(sc.Sport, sc.EID, sc.League, sc.HomeTeam, sc.AwayTeam)
	gc := game.NewGameContext(sc.Sport, sc.League, sc.EID, gs)

	if sc.Sport == events.SportSoccer {
		if ss, ok := gs.(*soccerState.SoccerState); ok {
			gc.PregameApplied = e.applySoccerPregame(ss, sc.HomeTeam, sc.AwayTeam)
		}
	}

	if e.resolver != nil {
		go e.resolveTickers(gc, sc, gameStartedAt)
	}

	return gc
}

// applySoccerPregame looks up pregame odds from the cache and applies them.
// Returns true if odds were successfully applied.
func (e *Engine) applySoccerPregame(ss *soccerState.SoccerState, homeTeam, awayTeam string) bool {
	if e.pregame == nil {
		return false
	}

	e.pregameMu.RLock()
	stale := time.Since(e.pregameFetch) > pregameCacheTTL
	cached := e.pregameCache
	e.pregameMu.RUnlock()

	if stale || cached == nil {
		go e.refreshPregameCache()
		if cached == nil {
			return false
		}
	}

	homeNorm := strings.ToLower(strings.TrimSpace(homeTeam))
	awayNorm := strings.ToLower(strings.TrimSpace(awayTeam))

	for _, p := range cached {
		pHome := strings.ToLower(strings.TrimSpace(p.HomeTeam))
		pAway := strings.ToLower(strings.TrimSpace(p.AwayTeam))

		if (fuzzyTeamMatch(pHome, homeNorm) && fuzzyTeamMatch(pAway, awayNorm)) ||
			(fuzzyTeamMatch(pHome, awayNorm) && fuzzyTeamMatch(pAway, homeNorm)) {
			ss.HomeWinPct = p.HomeWinPct
			ss.DrawPct = p.DrawPct
			ss.AwayWinPct = p.AwayWinPct
			ss.G0 = p.G0
			telemetry.Debugf("pregame: matched %s vs %s -> H=%.1f%% D=%.1f%% A=%.1f%% G0=%.2f",
				homeTeam, awayTeam, p.HomeWinPct*100, p.DrawPct*100, p.AwayWinPct*100, p.G0)
			return true
		}
	}
	telemetry.Debugf("pregame: no match for %s vs %s", homeTeam, awayTeam)
	return false
}

func (e *Engine) refreshPregameCache() {
	e.pregameMu.Lock()
	if e.pregameCache != nil && time.Since(e.pregameFetch) < pregameCacheTTL {
		e.pregameMu.Unlock()
		return
	}
	if time.Since(e.pregameLastTry) < pregameRetryCool {
		e.pregameMu.Unlock()
		return
	}
	e.pregameLastTry = time.Now()
	e.pregameMu.Unlock()

	odds, err := e.pregame.FetchSoccerPregame()
	if err != nil {
		telemetry.Warnf("pregame: fetch failed: %v", err)
		return
	}

	e.pregameMu.Lock()
	e.pregameCache = odds
	e.pregameFetch = time.Now()
	e.pregameMu.Unlock()
}

func fuzzyTeamMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.Contains(a, b) || strings.Contains(b, a)
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

const edgeThreshold = 3.0 // percentage points

func hasSignificantEdge(gc *game.GameContext) bool {
	switch ss := gc.Game.(type) {
	case *soccerState.SoccerState:
		if ss.PinnacleHomePct == nil || ss.PinnacleDrawPct == nil || ss.PinnacleAwayPct == nil {
			return false
		}
		pairs := []struct {
			pinn float64
			ask  float64
		}{
			{*ss.PinnacleHomePct, gc.YesAsk(ss.HomeTicker)},
			{*ss.PinnacleDrawPct, gc.YesAsk(ss.DrawTicker)},
			{*ss.PinnacleAwayPct, gc.YesAsk(ss.AwayTicker)},
			{100 - *ss.PinnacleHomePct, gc.NoAsk(ss.HomeTicker)},
			{100 - *ss.PinnacleDrawPct, gc.NoAsk(ss.DrawTicker)},
			{100 - *ss.PinnacleAwayPct, gc.NoAsk(ss.AwayTicker)},
		}
		for _, p := range pairs {
			if p.ask > 0 && p.pinn-p.ask >= edgeThreshold {
				return true
			}
		}
	}
	return false
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

// resolveTickers runs async (HTTP call), then sends results back to the game goroutine.
func (e *Engine) resolveTickers(gc *game.GameContext, sc events.ScoreChangeEvent, gameStartedAt time.Time) {
	resolved := e.resolver.Resolve(context.Background(), sc.Sport, sc.HomeTeam, sc.AwayTeam, gameStartedAt)
	if resolved == nil {
		telemetry.Debugf("ticker: no match for %s %s vs %s", sc.Sport, sc.HomeTeam, sc.AwayTeam)
		return
	}

	gc.Send(func() {
		gc.Game.SetTickers(resolved.HomeTicker, resolved.AwayTicker, resolved.DrawTicker)
		gc.KalshiEventURL = ticker.KalshiEventURL(resolved.EventTicker)

		for _, t := range resolved.AllTickers() {
			td := &game.TickerData{Ticker: t}
			if snap, ok := resolved.Prices[t]; ok {
				td.YesAsk = float64(snap.YesAsk)
				td.YesBid = float64(snap.YesBid)
				if snap.YesBid > 0 {
					td.NoAsk = float64(100 - snap.YesBid)
				}
				if snap.YesAsk > 0 {
					td.NoBid = float64(100 - snap.YesAsk)
				}
				td.Volume = snap.Volume
			}
			gc.Tickers[t] = td
		}

		if !gc.DisplayedLive && gc.Game.HasLiveData() {
			gc.DisplayedLive = true
			if time.Since(e.startedAt) < 10*time.Second {
				printGame(gc, "LIVE")
			} else {
				printGame(gc, "GAME-START")
			}
		}
	})
}
