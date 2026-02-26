package store

import (
	"sync"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/events"
)

// GameKey uniquely identifies a game across sports.
type GameKey struct {
	Sport  events.Sport
	GameID string // GoalServe EID, empty until bound by first live event
}

// TeamKey identifies a game by its canonical team pair (normalized names).
type TeamKey struct {
	Sport    events.Sport
	HomeNorm string
	AwayNorm string
}

// TeamLookupResult is returned by GetByTeams.
type TeamLookupResult struct {
	GC *game.GameContext
}

// GameStateStore is a thread-safe map of all active game contexts.
//
// Games are indexed two ways:
//   - By (sport, EID) in the games map — fast path after EID binding
//   - By (sport, homeNorm, awayNorm) in teamIndex — used for initial
//     team-name routing before the EID is known
//
// The store's RWMutex protects the maps themselves (lookups, inserts, deletes).
// It does NOT protect the GameContext contents — each GameContext
// serializes its own state mutations through its inbox channel.
type GameStateStore struct {
	mu          sync.RWMutex
	games       map[GameKey]*game.GameContext
	teamIndex   map[TeamKey]*game.GameContext
	tickerIndex map[string][]*game.GameContext
}

func New() *GameStateStore {
	return &GameStateStore{
		games:       make(map[GameKey]*game.GameContext),
		teamIndex:   make(map[TeamKey]*game.GameContext),
		tickerIndex: make(map[string][]*game.GameContext),
	}
}

func (s *GameStateStore) Get(sport events.Sport, gameID string) (*game.GameContext, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	gc, ok := s.games[GameKey{Sport: sport, GameID: gameID}]
	return gc, ok
}

func (s *GameStateStore) Put(gc *game.GameContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gc.EID != "" {
		s.games[GameKey{Sport: gc.Sport, GameID: gc.EID}] = gc
	}
	if gc.HomeTeamNorm != "" && gc.AwayTeamNorm != "" {
		s.teamIndex[TeamKey{Sport: gc.Sport, HomeNorm: gc.HomeTeamNorm, AwayNorm: gc.AwayTeamNorm}] = gc
	}
}

// GetByTeams looks up a GameContext by normalized team names. It tries
// both orientations (home/away and away/home) and reports which matched.
// Returns nil result if no match is found.
func (s *GameStateStore) GetByTeams(sport events.Sport, homeNorm, awayNorm string) *TeamLookupResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if gc, ok := s.teamIndex[TeamKey{Sport: sport, HomeNorm: homeNorm, AwayNorm: awayNorm}]; ok {
		return &TeamLookupResult{GC: gc}
	}
	if gc, ok := s.teamIndex[TeamKey{Sport: sport, HomeNorm: awayNorm, AwayNorm: homeNorm}]; ok {
		return &TeamLookupResult{GC: gc}
	}
	return nil
}

// BindEID associates a GoalServe EID with an existing GameContext so
// subsequent lookups via Get() resolve in O(1).
func (s *GameStateStore) BindEID(gc *game.GameContext, eid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gc.EID = eid
	s.games[GameKey{Sport: gc.Sport, GameID: eid}] = gc
}

// Delete removes a game from the store and shuts down its goroutine.
func (s *GameStateStore) Delete(sport events.Sport, gameID string) {
	s.mu.Lock()
	key := GameKey{Sport: sport, GameID: gameID}
	gc, ok := s.games[key]
	delete(s.games, key)
	if ok {
		if gc.HomeTeamNorm != "" && gc.AwayTeamNorm != "" {
			delete(s.teamIndex, TeamKey{
				Sport: gc.Sport, HomeNorm: gc.HomeTeamNorm, AwayNorm: gc.AwayTeamNorm,
			})
		}
		for ticker, gcs := range s.tickerIndex {
			for i, g := range gcs {
				if g == gc {
					s.tickerIndex[ticker] = append(gcs[:i], gcs[i+1:]...)
					break
				}
			}
			if len(s.tickerIndex[ticker]) == 0 {
				delete(s.tickerIndex, ticker)
			}
		}
	}
	s.mu.Unlock()

	if ok {
		gc.Close()
	}
}

// RegisterTicker associates a Kalshi ticker with a game context so
// market data updates can be routed directly without iterating all games.
func (s *GameStateStore) RegisterTicker(ticker string, gc *game.GameContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.tickerIndex[ticker] {
		if existing == gc {
			return
		}
	}
	s.tickerIndex[ticker] = append(s.tickerIndex[ticker], gc)
}

// ByTicker returns all game contexts that have registered interest in a
// specific Kalshi ticker. Returns nil if no games match.
// The returned slice is a snapshot safe to iterate after the lock is released.
func (s *GameStateStore) ByTicker(ticker string) []*game.GameContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.tickerIndex[ticker]
	if len(src) == 0 {
		return nil
	}
	out := make([]*game.GameContext, len(src))
	copy(out, src)
	return out
}

// All returns a snapshot of all game contexts. Safe for iteration.
func (s *GameStateStore) All() []*game.GameContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*game.GameContext, 0, len(s.games))
	for _, gc := range s.games {
		out = append(out, gc)
	}
	return out
}

// BySport returns all game contexts for a given sport, including
// unbound GameContexts that only exist in teamIndex (no EID yet).
func (s *GameStateStore) BySport(sport events.Sport) []*game.GameContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[*game.GameContext]bool)
	var out []*game.GameContext
	for k, gc := range s.games {
		if k.Sport == sport {
			out = append(out, gc)
			seen[gc] = true
		}
	}
	for k, gc := range s.teamIndex {
		if k.Sport == sport && !seen[gc] {
			out = append(out, gc)
		}
	}
	return out
}

func (s *GameStateStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.games)
}
