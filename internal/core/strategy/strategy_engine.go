package strategy

import (
	"context"
	"strings"
	"sync"
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
	observers  []game.GameObserver

	kalshiWSUp  atomic.Bool
	skippedEIDs sync.Map
}

func NewEngine(bus *events.Bus, gameStore *store.GameStateStore, registry *Registry, resolver *ticker.Resolver, subscriber TickerSubscriber, observers []game.GameObserver) *Engine {
	e := &Engine{
		bus:        bus,
		store:      gameStore,
		registry:   registry,
		resolver:   resolver,
		subscriber: subscriber,
		display:    display.NewTracker(),
		observers:  observers,
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
		if _, tried := e.skippedEIDs.LoadOrStore(gu.EID, struct{}{}); tried {
			return nil
		}
		go e.resolveAndCreate(gu, evt)
		return nil
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

			strat, ok := e.registry.Get(gu.Sport)
			if !ok {
				return
			}

			intents := strat.OnFinish(gc, &gu)
			e.publishIntents(intents, gu.Sport, gu.League, gu.EID, evt.Timestamp)

			defer gc.SetMatchStatus(events.StatusGameFinish)
			return
		}

		// ── LIVE path ───────────────────────────────────────────
		strat, ok := e.registry.Get(gu.Sport)
		if !ok {
			return
		}

		hadLIVEData := gc.Game.HasLIVEData()
		prevHome := gc.Game.GetHomeScore()
		prevAway := gc.Game.GetAwayScore()

		result := strat.Evaluate(gc, &gu)

		e.publishIntents(result.Intents, gu.Sport, gu.League, gu.EID, evt.Timestamp)

		// ── MatchStatus ─────────────────────────────────────────
		scoreChanged := hadLIVEData && (gc.Game.GetHomeScore() != prevHome || gc.Game.GetAwayScore() != prevAway)
		status := gu.MatchStatus
		if scoreChanged {
			status = events.StatusScoreChange
		} else if status == events.StatusGameStart {
			ds := e.display.Get(gc.EID)
			if ds.GameStarted {
				status = events.StatusLive
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

		if status == events.StatusLive {
			ds := e.display.Get(gc.EID)
			if !ds.DisplayedLIVE {
				ds.DisplayedLIVE = true
				gc.SetMatchStatus(events.StatusLive)
			}
		} else {
			ds := e.display.Get(gc.EID)
			if !ds.DisplayedLIVE {
				ds.DisplayedLIVE = true
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
		"after over time", "after OVERTIME", "after ot",
		"after extra time", "aet",
		"after penalties", "after pen",
	} {
		if low == tok || strings.Contains(low, tok) {
			return true
		}
	}
	return false
}

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

			if !gc.Game.HasLIVEData() || !ds.DisplayedLIVE {
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

			gc.Notify("PRICE_UPDATE")
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
		telemetry.Infof("strategy: Kalshi WS connected, waiting for LIVE prices")
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

// resolveAndCreate fuzzy-matches a GoalServe game against the Kalshi
// market cache. A GameContext is only created when both sides match —
// not by a Kalshi ticker alone, not by a GoalServe webhook alone.
// On failure, the EID stays in skippedEIDs so future webhooks skip instantly.
func (e *Engine) resolveAndCreate(gu events.GameUpdateEvent, evt events.Event) {
	if e.resolver == nil {
		return
	}

	gameStart := evt.Timestamp
	if gu.GameStartUTC > 0 {
		gameStart = time.Unix(gu.GameStartUTC, 0)
	}

	resolved := e.resolver.Resolve(context.Background(), gu.Sport, gu.HomeTeam, gu.AwayTeam, gameStart)
	if resolved == nil {
		telemetry.Debugf("ticker: no match for %s %s vs %s", gu.Sport, gu.HomeTeam, gu.AwayTeam)
		return
	}

	gs := e.registry.CreateGameState(gu.Sport, gu.EID, gu.League, gu.HomeTeam, gu.AwayTeam)
	gc := game.NewGameContext(gu.Sport, gu.League, gu.EID, gs)
	gc.GameStartedAt = gameStart

	for _, o := range e.observers {
		gc.AddObserver(o)
	}

	allTickers := resolved.AllTickers()
	if e.subscriber != nil {
		if err := e.subscriber.SubscribeTickers(allTickers); err != nil {
			telemetry.Warnf("ticker: WS subscribe failed for %s vs %s: %v", gu.HomeTeam, gu.AwayTeam, err)
		}
	}

	initialStatus := gu.MatchStatus
	if initialStatus == "" {
		initialStatus = "LIVE"
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
		if initialStatus == "GAME START" {
			ds.GameStarted = true
		}
		if gc.Game.HasPregame() {
			gc.Game.RecalcEdge(gc.Tickers)
			gc.SetMatchStatus(initialStatus)
		}
	})

	e.store.Put(gc)
	telemetry.Metrics.ActiveGames.Inc()
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
