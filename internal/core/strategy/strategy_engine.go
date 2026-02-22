package strategy

import (
	"context"
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
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
	bus      *events.Bus
	store    *store.GameStateStore
	registry *Registry
	resolver *ticker.Resolver
}

func NewEngine(bus *events.Bus, gameStore *store.GameStateStore, registry *Registry, resolver *ticker.Resolver) *Engine {
	e := &Engine{
		bus:      bus,
		store:    gameStore,
		registry: registry,
		resolver: resolver,
	}

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

		if !gc.DisplayedLive && (firstLive || scoreChanged) {
			gc.DisplayedLive = true
			if !gc.GameStartedAt.IsZero() && time.Since(gc.GameStartedAt) > 5*time.Minute {
				printGame(gc, "LIVE")
			} else {
				printGame(gc, "GAME-START")
			}
		} else if scoreChanged {
			printGame(gc, "GOAL")
		}

		for _, evt := range result.DisplayEvents {
			printGame(gc, evt)
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
			if strat.HasSignificantEdge(gc) {
				gc.LastEdgeDisplay = time.Now()
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
	if connected {
		telemetry.Infof("strategy: Kalshi WS reconnected, restoring live prices")
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
		gc.KalshiConnected = true
		gc.Game.SetTickers(resolved.HomeTicker, resolved.AwayTicker, resolved.DrawTicker)
		gc.KalshiEventURL = ticker.KalshiEventURL(resolved.EventTicker)

		for _, t := range resolved.AllTickers() {
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

		if !gc.DisplayedLive && gc.Game.HasLiveData() {
			gc.DisplayedLive = true
			if !gc.GameStartedAt.IsZero() && time.Since(gc.GameStartedAt) > 5*time.Minute {
				printGame(gc, "LIVE")
			} else {
				printGame(gc, "GAME-START")
			}
		}
	})
}
