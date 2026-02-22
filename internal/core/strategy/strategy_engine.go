package strategy

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/ticker"
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

		if len(gc.Tickers) == 0 {
			return
		}

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

		if ds.DisplayedLive {
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

	noAsk := me.NoAsk
	noBid := me.NoBid
	if noAsk == 0 {
		noAsk = 100
	}
	if noBid == 0 {
		noBid = 100
	}

	td := &game.TickerData{
		Ticker: me.Ticker,
		YesAsk: me.YesAsk,
		YesBid: me.YesBid,
		NoAsk:  noAsk,
		NoBid:  noBid,
		Volume: me.Volume,
	}

	targets := e.store.ByTicker(me.Ticker)
	if len(targets) == 0 {
		return nil
	}

	for _, gc := range targets {
		gc.Send(func() {
			gc.KalshiConnected = true
			gc.UpdateTicker(td)

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
				if snap.YesBid > 0 {
					td.NoAsk = float64(100 - snap.YesBid)
				}
				if snap.YesAsk > 0 {
					td.NoBid = float64(100 - snap.YesAsk)
				}
				td.Volume = snap.Volume
			}
			gc.Tickers[t] = td
			e.store.RegisterTicker(t, gc)
		}

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
