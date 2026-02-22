package strategy

import (
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/game/football"
	"github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/events"
)

// EvalResult is returned by Evaluate. It carries order intents plus
// optional display event names (e.g. "RED-CARD") that the engine
// should trigger after the standard LIVE/GOAL/GAME-START display logic.
type EvalResult struct {
	Intents       []events.OrderIntent
	DisplayEvents []string
}

// Strategy is the interface each sport must implement.
type Strategy interface {
	// Evaluate is called on each score change.
	Evaluate(gc *game.GameContext, sc *events.ScoreChangeEvent) EvalResult

	// OnPriceUpdate is called when a Kalshi market price changes.
	OnPriceUpdate(gc *game.GameContext) []events.OrderIntent

	// OnFinish is called when a game ends. Returns "slam" orders for settled markets.
	OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent

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
