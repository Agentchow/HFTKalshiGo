package strategy

import (
	"context"
	"time"

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
		strat, ok := e.registry.Get(sc.Sport)
		if !ok {
			return
		}

		intents := strat.Evaluate(gc, &sc)
		for _, intent := range intents {
			telemetry.Metrics.OrderIntents.Inc()
			e.bus.Publish(events.Event{
				ID:        intent.Ticker,
				Type:      events.EventOrderIntent,
				Sport:     sc.Sport,
				League:    sc.League,
				GameID:    sc.EID,
				Timestamp: evt.Timestamp,
				Payload:   intent,
			})
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
		strat, ok := e.registry.Get(gf.Sport)
		if !ok {
			return
		}

		intents := strat.OnFinish(gc, &gf)
		for _, intent := range intents {
			telemetry.Metrics.OrderIntents.Inc()
			e.bus.Publish(events.Event{
				ID:        intent.Ticker,
				Type:      events.EventOrderIntent,
				Sport:     gf.Sport,
				League:    gf.League,
				GameID:    gf.EID,
				Timestamp: evt.Timestamp,
				Payload:   intent,
			})
		}

		telemetry.Metrics.ActiveGames.Dec()
	})

	return nil
}

// onMarketData routes Kalshi WS price updates to the correct game.
// The WS frame only contains a ticker string — we scan all games
// to find which one owns it.
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
		// Tickers are seeded asynchronously by the resolver, so
		// we forward every update to the game goroutine and let it
		// check ownership there (race-free).
		gc.Send(func() {
			if _, exists := gc.Tickers[me.Ticker]; exists {
				gc.UpdateTicker(td)
			}
		})
	}
	return nil
}

func (e *Engine) createGameContext(sc events.ScoreChangeEvent, gameStartedAt time.Time) *game.GameContext {
	gs := e.registry.CreateGameState(sc.Sport, sc.EID, sc.League, sc.HomeTeam, sc.AwayTeam)
	gc := game.NewGameContext(sc.Sport, sc.League, sc.EID, gs)

	if e.resolver != nil {
		go e.resolveTickers(gc, sc, gameStartedAt)
	}

	return gc
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

		for _, t := range resolved.AllTickers() {
			gc.Tickers[t] = &game.TickerData{Ticker: t}
		}

		telemetry.Infof("ticker: resolved %s %s vs %s -> home=%s away=%s draw=%s",
			sc.Sport, sc.HomeTeam, sc.AwayTeam,
			resolved.HomeTicker, resolved.AwayTicker, resolved.DrawTicker)
	})
}
