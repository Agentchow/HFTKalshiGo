package strategy

import (
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/game/football"
	"github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/events"
)

// EvalResult is returned by Evaluate. It carries order intents for the
// engine to publish. Sport-specific side effects (red card, power play
// callbacks) are fired directly inside Evaluate.
type EvalResult struct {
	Intents []events.OrderIntent
}

// Strategy is the interface each sport must implement.
type Strategy interface {
	// Evaluate is called on each game update (live webhook).
	Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) EvalResult

	// OnPriceUpdate is called when a Kalshi market price changes.
	OnPriceUpdate(gc *game.GameContext) []events.OrderIntent

	// OnFinish is called when a game ends. Returns "slam" orders for settled markets.
	OnFinish(gc *game.GameContext, gu *events.GameUpdateEvent) []events.OrderIntent

	// HasSignificantEdge returns true when the game has a model-vs-market
	// edge worth displaying. Used by the engine for throttled EDGE prints.
	HasSignificantEdge(gc *game.GameContext) bool
}

// Registry maps sport -> strategy implementation.
type Registry struct {
	strategies map[events.Sport]Strategy
}

func NewRegistry() *Registry {
	return &Registry{
		strategies: make(map[events.Sport]Strategy),
	}
}

func (r *Registry) Register(sport events.Sport, s Strategy) {
	r.strategies[sport] = s
}

func (r *Registry) Get(sport events.Sport) (Strategy, bool) {
	s, ok := r.strategies[sport]
	return s, ok
}

// CreateGameState is a factory that produces the correct sport-specific
// GameState implementation for a new game.
func (r *Registry) CreateGameState(sport events.Sport, eid, league, home, away string) game.GameState {
	switch sport {
	case events.SportHockey:
		return hockey.New(eid, league, home, away)
	case events.SportSoccer:
		return soccer.New(eid, league, home, away)
	case events.SportFootball:
		return football.New(eid, league, home, away)
	default:
		return hockey.New(eid, league, home, away)
	}
}
