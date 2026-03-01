package game

import (
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// TickerData holds the latest bid/ask snapshot for a single Kalshi ticker.
type TickerData struct {
	Ticker string
	YesAsk float64
	YesBid float64
	NoAsk  float64
	NoBid  float64
	Volume int64
}

// Fill records a single filled order against this game's exposure.
type Fill struct {
	OrderID   string
	Ticker    string
	Side      string
	CostCents int
}

// OverturnInfo captures the score before and after an overturn event.
// Set by the strategy on GameContext.LastOverturn before calling Notify,
// so observers can read the details without digging into sport state.
type OverturnInfo struct {
	OldHome int
	OldAway int
	NewHome int
	NewAway int
}

// GameContext is the single source of truth for one game.
//
// GameContexts are eagerly created at startup from Kalshi markets matched
// against GoalServe pregame odds. The EID is initially empty and gets
// bound when the first live event arrives and is matched by team name.
//
// All state mutations are serialized through an inbox channel — one
// goroutine drains it, so no mutexes are needed on any field.
//
// Any goroutine that wants to read or write game state sends a closure
// via Send(). The closure runs on the game's own goroutine.
type GameContext struct {
	Sport  events.Sport
	League string
	EID    string // GoalServe EID, empty until first live event binds it

	// Canonical team names (normalized) from the pregame HTTP source of truth.
	HomeTeamNorm string
	AwayTeamNorm string

	// Sport-specific state (scores, period, model output, tickers).
	Game GameState

	MatchStatus events.MatchStatus

	// LIVE market prices keyed by Kalshi ticker.
	Tickers map[string]*TickerData

	// Fills recorded against this game.
	Fills []Fill

	// KalshiEventURL is the link to the Kalshi event page for this game.
	KalshiEventURL string

	// KalshiConnected is true when the Kalshi WS feed is LIVE.
	// When false, ticker prices are stale and should not be displayed.
	KalshiConnected bool

	// LastOverturn is set by the strategy before notifying observers of
	// an overturn event. Nil when the current event is not an overturn.
	LastOverturn *OverturnInfo

	// LastScorer is "home" or "away" after a score change, "" otherwise.
	// Set by the engine before notifying observers of SCORE CHANGE.
	LastScorer string

	// GameStartedAt is the actual kickoff / puck-drop time from GoalServe.
	GameStartedAt time.Time

	observers []GameObserver

	inbox chan func()
	stop  chan struct{}
}

// GameObserver receives notifications when game state changes.
// Implementations run on the game's goroutine — safe to read gc fields directly.
type GameObserver interface {
	OnGameEvent(gc *GameContext, eventType string)
}

// GameState is the interface every sport-specific state must implement.
type GameState interface {
	GetEID() string
	GetHomeTeam() string
	GetAwayTeam() string
	GetHomeScore() int
	GetAwayScore() int
	GetPeriod() string
	GetTimeRemaining() float64
	IsFinished() bool
	IsLIVE() bool
	HasLIVEData() bool

	UpdateGameState(homeScore, awayScore int, period string, timeRemain float64) bool
	CheckScoreDrop(homeScore, awayScore int, confirmSec int) string
	ClearScoreDropPending()
	IsScoreDropPending() bool
	SetTickers(home, away, draw string)
	HasPregame() bool

	// DeduplicateStatus suppresses repeated one-shot display statuses.
	// e.g. hockey returns StatusLive after the first StatusOvertime notification.
	DeduplicateStatus(status events.MatchStatus) events.MatchStatus

	// RecalcEdge recomputes model-vs-market edge from the current
	// model probabilities and Kalshi ticker prices.
	RecalcEdge(tickers map[string]*TickerData)

	// HasSignificantEdge returns true when the game has a model-vs-market
	// edge worth displaying.
	HasSignificantEdge() bool
}

func NewGameContext(sport events.Sport, league, eid string, gs GameState) *GameContext {
	gc := &GameContext{
		Sport:   sport,
		League:  league,
		EID:     eid,
		Game:    gs,
		Tickers: make(map[string]*TickerData),
		inbox:   make(chan func(), 256),
		stop:    make(chan struct{}),
	}
	go gc.run()
	return gc
}

// run is the game's event loop. All closures sent via Send() execute
// here, one at a time, on this single goroutine. No locks needed.
func (gc *GameContext) run() {
	defer close(gc.stop)
	for fn := range gc.inbox {
		fn()
	}
}

// Send enqueues a closure to run on the game's goroutine.
// Non-blocking: drops the closure and logs a warning if the inbox is full,
// preventing a stuck game from blocking upstream event processing.
func (gc *GameContext) Send(fn func()) {
	select {
	case gc.inbox <- fn:
	default:
		telemetry.Metrics.InboxOverflows.Inc()
		telemetry.Warnf("game %s: inbox full (cap=%d), dropping event", gc.EID, cap(gc.inbox))
	}
}

// AddObserver registers an observer that will be notified on game events.
// Must be called before the game starts receiving events.
func (gc *GameContext) AddObserver(o GameObserver) {
	gc.observers = append(gc.observers, o)
}

// Notify calls all registered observers with the given event type.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) Notify(eventType string) {
	for _, o := range gc.observers {
		o.OnGameEvent(gc, eventType)
	}
}

// SetMatchStatus updates the match status and notifies all observers.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) SetMatchStatus(status events.MatchStatus) {
	gc.MatchStatus = status
	gc.Notify(string(status))
}

// Close shuts down the game's goroutine and waits for it to drain.
func (gc *GameContext) Close() {
	close(gc.inbox)
	<-gc.stop
}

// UpdateTicker sets or replaces the LIVE market snapshot for a ticker.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) UpdateTicker(td *TickerData) {
	gc.Tickers[td.Ticker] = td
}

// YesAsk returns the yes-side ask for a ticker (cents), or -1 if unavailable.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) YesAsk(ticker string) float64 {
	if td, ok := gc.Tickers[ticker]; ok {
		return td.YesAsk
	}
	return -1
}

// NoAsk returns the no-side ask for a ticker (cents), or -1 if unavailable.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) NoAsk(ticker string) float64 {
	if td, ok := gc.Tickers[ticker]; ok {
		return td.NoAsk
	}
	return -1
}

// RecordFill appends a fill.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) RecordFill(f Fill) {
	gc.Fills = append(gc.Fills, f)
}

// TotalVolume sums volume across all tickers for this game.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) TotalVolume() int64 {
	var total int64
	for _, td := range gc.Tickers {
		total += td.Volume
	}
	return total
}

// TotalExposureCents sums all fill costs for this game.
// Must be called from the game's goroutine (inside a Send closure).
func (gc *GameContext) TotalExposureCents() int {
	total := 0
	for _, f := range gc.Fills {
		total += f.CostCents
	}
	return total
}
