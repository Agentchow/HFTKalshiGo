package strategy

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/odds"
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

// PregameProvider fetches pregame odds for a sport. Implementations
// wrap goalserve_http.PregameClient.FetchSoccerPregame / FetchHockeyPregame.
type PregameProvider func() ([]odds.PregameOdds, error)

const (
	refreshInterval    = 1 * time.Hour
	initMaxAttempts    = 5
	initRetryBase      = 10 * time.Second
	refreshBackoffBase = 10 * time.Second
	refreshBackoffMax  = 5 * time.Minute
)

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

	kalshiWSUp atomic.Bool
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

	// Fast path: EID already bound from a previous event.
	gc, exists := e.store.Get(gu.Sport, gu.EID)

	// Slow path: first event for this game — match by team name.
	if !exists {
		aliases := ticker.AliasesForSport(gu.Sport)
		homeNorm := ticker.Normalize(gu.HomeTeam, aliases)
		awayNorm := ticker.Normalize(gu.AwayTeam, aliases)

		result := e.store.GetByTeams(gu.Sport, homeNorm, awayNorm)
		if result == nil {
			result = e.fuzzyTeamLookup(gu.Sport, homeNorm, awayNorm)
		}
		if result == nil {
			telemetry.Debugf("engine: unmatched %s event eid=%s  %q vs %q  (norm: %q vs %q)  period=%s",
				gu.Sport, gu.EID, gu.HomeTeam, gu.AwayTeam, homeNorm, awayNorm, gu.Period)
			return nil
		}

		gc = result.GC
		gc.League = gu.League
		e.store.BindEID(gc, gu.EID)
		gc.Send(func() {
			if id, ok := gc.Game.(interface{ SetIdentifiers(string, string) }); ok {
				id.SetIdentifiers(gu.EID, gu.League)
			}
		})
		telemetry.Infof("engine: bound EID %s to %s vs %s",
			gu.EID, gc.HomeTeamNorm, gc.AwayTeamNorm)
	}

	if e.needsSwap(gc, &gu) {
		swapEventFields(&gu)
	}

	gc.Send(func() {
		// ── Finish path ─────────────────────────────────────────
		if isFinished(gu.Period) {
			ds := e.display.Get(gc.EID)
			if ds.Finaled {
				return
			}
			ds.Finaled = true
			defer gc.SetMatchStatus(events.StatusGameFinish)
			telemetry.Metrics.ActiveGames.Dec()

			gc.Game.UpdateGameState(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
			gc.Game.RecalcEdge(gc.Tickers)

			strat, ok := e.registry.Get(gu.Sport)
			if !ok {
				return
			}

			intents := strat.OnFinish(gc, &gu)
			e.publishIntents(intents, gu.Sport, gu.League, gu.EID, evt.Timestamp)
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

		if result.Finished {
			ds := e.display.Get(gc.EID)
			if !ds.Finaled {
				ds.Finaled = true
				telemetry.Metrics.ActiveGames.Dec()
				gc.SetMatchStatus(events.StatusGameFinish)
			}
			return
		}

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

// swapEventFields flips all positional fields in a GameUpdateEvent so the
// strategy always sees data in the canonical (pregame) orientation.
func swapEventFields(gu *events.GameUpdateEvent) {
	gu.HomeTeam, gu.AwayTeam = gu.AwayTeam, gu.HomeTeam
	gu.HomeScore, gu.AwayScore = gu.AwayScore, gu.HomeScore
	gu.HomeRedCards, gu.AwayRedCards = gu.AwayRedCards, gu.HomeRedCards
	gu.HomePenaltyCount, gu.AwayPenaltyCount = gu.AwayPenaltyCount, gu.HomePenaltyCount
}

// needsSwap determines per-event whether the live feed's home/away orientation
// is reversed relative to canonical (pregame) orientation. Tries exact match
// first, then falls back to fuzzy substring matching.
func (e *Engine) needsSwap(gc *game.GameContext, gu *events.GameUpdateEvent) bool {
	aliases := ticker.AliasesForSport(gu.Sport)
	eventHomeNorm := ticker.Normalize(gu.HomeTeam, aliases)
	if eventHomeNorm == gc.HomeTeamNorm {
		return false
	}
	if eventHomeNorm == gc.AwayTeamNorm {
		return true
	}
	return ticker.FuzzyContains(gc.AwayTeamNorm, eventHomeNorm)
}

// fuzzyTeamLookup is the fallback when exact normalized names don't match.
// It iterates all GameContexts for the sport and tries fuzzy substring matching.
func (e *Engine) fuzzyTeamLookup(sport events.Sport, homeNorm, awayNorm string) *store.TeamLookupResult {
	for _, gc := range e.store.BySport(sport) {
		if gc.HomeTeamNorm == "" {
			continue
		}
		sameOrder := ticker.FuzzyContains(gc.HomeTeamNorm, homeNorm) && ticker.FuzzyContains(gc.AwayTeamNorm, awayNorm)
		swapped := ticker.FuzzyContains(gc.HomeTeamNorm, awayNorm) && ticker.FuzzyContains(gc.AwayTeamNorm, homeNorm)
		if sameOrder || swapped {
			return &store.TeamLookupResult{GC: gc}
		}
	}
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

// InitializeGames eagerly creates GameContexts by matching GoalServe
// pregame entries against Kalshi markets. This blocks until complete;
// the fanout connection should not be established until this returns.
func (e *Engine) InitializeGames(ctx context.Context, sport events.Sport, provider PregameProvider) {
	if err := e.resolver.RefreshMarkets(ctx, sport); err != nil {
		telemetry.Errorf("engine: failed to refresh Kalshi markets: %v", err)
	}

	pregame := e.fetchPregameWithRetry(provider)
	if pregame == nil {
		telemetry.Errorf("engine: all pregame fetch attempts failed — no games initialized")
		return
	}

	created := 0
	matched := make(map[string]bool)
	aliases := ticker.AliasesForSport(sport)
	for _, p := range pregame {
		if et := e.initializeGame(ctx, sport, p, aliases); et != "" {
			matched[et] = true
			created++
		}
	}

	telemetry.Infof("engine: initialized %d games for %s (from %d pregame entries)", created, sport, len(pregame))

	for _, ue := range e.resolver.UnmatchedKalshiEvents(sport, matched) {
		telemetry.Warnf("engine: Kalshi event %s has no matching pregame data (%s vs %s)",
			ue.EventTicker, ue.Home, ue.Away)
	}

	go e.startPeriodicRefresh(ctx, sport, provider)
}

// fetchPregameWithRetry attempts to fetch pregame odds with exponential backoff.
func (e *Engine) fetchPregameWithRetry(provider PregameProvider) []odds.PregameOdds {
	delay := initRetryBase
	for attempt := 1; attempt <= initMaxAttempts; attempt++ {
		fetched, err := provider()
		if err != nil {
			telemetry.Warnf("pregame: startup fetch attempt %d/%d failed: %v", attempt, initMaxAttempts, err)
			if attempt < initMaxAttempts {
				time.Sleep(delay)
				delay *= 2
			}
			continue
		}
		telemetry.Infof("pregame: loaded %d matches on startup", len(fetched))
		return fetched
	}
	return nil
}

// initializeGame matches one pregame entry against Kalshi markets and creates
// a GameContext if a match is found. Returns the matched Kalshi event ticker,
// or "" if no match was found.
func (e *Engine) initializeGame(ctx context.Context, sport events.Sport, p odds.PregameOdds, aliases map[string]string) string {
	homeNorm := ticker.Normalize(p.HomeTeam, aliases)
	awayNorm := ticker.Normalize(p.AwayTeam, aliases)
	if homeNorm == "" || awayNorm == "" {
		return ""
	}

	// Skip if we already have a GameContext for this team pair.
	if existing := e.store.GetByTeams(sport, homeNorm, awayNorm); existing != nil {
		return ""
	}

	resolved := e.resolver.Resolve(ctx, sport, p.HomeTeam, p.AwayTeam, time.Now())
	if resolved == nil {
		return ""
	}

	// Pregame is the source of truth for orientation — no swap detection needed.
	gs := e.registry.CreateGameState(sport, "", "", p.HomeTeam, p.AwayTeam)
	gc := game.NewGameContext(sport, "", "", gs)
	gc.HomeTeamNorm = homeNorm
	gc.AwayTeamNorm = awayNorm

	e.applyPregameToState(gc, sport, p)

	for _, o := range e.observers {
		gc.AddObserver(o)
	}

	allTickers := resolved.AllTickers()
	if e.subscriber != nil {
		if err := e.subscriber.SubscribeTickers(allTickers); err != nil {
			telemetry.Warnf("ticker: WS subscribe failed for %s vs %s: %v", p.HomeTeam, p.AwayTeam, err)
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
	})

	e.store.Put(gc)
	telemetry.Metrics.ActiveGames.Inc()
	telemetry.Infof("[Created] GameContext \"%s\" vs \"%s\"", p.HomeTeam, p.AwayTeam)
	return resolved.EventTicker
}

// applyPregameToState writes pregame odds directly onto the sport-specific
// GameState. Since the pregame HTTP is the source of truth for orientation,
// HomePregameStrength always maps to our canonical home — no swap needed.
func (e *Engine) applyPregameToState(gc *game.GameContext, sport events.Sport, p odds.PregameOdds) {
	gc.Send(func() {
		gs := gc.Game
		type pregameSetter interface {
			SetPregame(home, away, draw, g0 float64)
		}
		if ps, ok := gs.(pregameSetter); ok {
			ps.SetPregame(p.HomePregameStrength, p.AwayPregameStrength, p.DrawPct, p.G0)
		}
	})
}

// startPeriodicRefresh re-fetches Kalshi markets and GoalServe pregame odds
// every refreshInterval, creating GameContexts for any new matches.
func (e *Engine) startPeriodicRefresh(ctx context.Context, sport events.Sport, provider PregameProvider) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()

	backoff := refreshBackoffBase
	aliases := ticker.AliasesForSport(sport)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		if err := e.resolver.RefreshMarkets(ctx, sport); err != nil {
			telemetry.Warnf("refresh: Kalshi markets fetch failed: %v", err)
		}

		pregame, err := provider()
		if err != nil {
			telemetry.Warnf("refresh: pregame fetch failed (backoff %v): %v", backoff, err)
			time.Sleep(backoff)
			backoff = min(backoff*2, refreshBackoffMax)
			continue
		}
		backoff = refreshBackoffBase

		created := 0
		for _, p := range pregame {
			if et := e.initializeGame(ctx, sport, p, aliases); et != "" {
				created++
			}
		}
		if created > 0 {
			telemetry.Infof("refresh: created %d new games for %s", created, sport)
		}
	}
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
