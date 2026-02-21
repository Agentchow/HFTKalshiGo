package strategy

import (
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/store"
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
}

func NewEngine(bus *events.Bus, gameStore *store.GameStateStore, registry *Registry) *Engine {
	e := &Engine{
		bus:      bus,
		store:    gameStore,
		registry: registry,
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
		gc = e.createGameContext(sc)
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

	for _, gc := range e.store.All() {
		// Check if this game has the ticker registered.
		// Tickers map is only written on the game's goroutine, but
		// we only read the key set here — safe because keys are set
		// once at creation and never deleted during the game's lifetime.
		if _, exists := gc.Tickers[me.Ticker]; exists {
			gc.Send(func() {
				gc.UpdateTicker(&game.TickerData{
					Ticker: me.Ticker,
					YesAsk: me.YesAsk,
					YesBid: me.YesBid,
					NoAsk:  me.NoAsk,
					NoBid:  me.NoBid,
					Volume: me.Volume,
				})
			})
			return nil
		}
	}
	return nil
}

func (e *Engine) createGameContext(sc events.ScoreChangeEvent) *game.GameContext {
	gs := e.registry.CreateGameState(sc.Sport, sc.EID, sc.League, sc.HomeTeam, sc.AwayTeam)
	return game.NewGameContext(sc.Sport, sc.League, sc.EID, gs)
}
