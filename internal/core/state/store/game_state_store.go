package store

import (
	"sync"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/events"
)

// GameKey uniquely identifies a game across sports.
type GameKey struct {
	Sport  events.Sport
	GameID string // GoalServe EID or Genius fixture ID
}

// GameStateStore is a thread-safe map of all active game contexts.
// Keyed by (sport, game_id).
//
// The store's RWMutex protects the map itself (lookups, inserts, deletes).
// It does NOT protect the GameContext contents — each GameContext
// serializes its own state mutations through its inbox channel.
type GameStateStore struct {
	mu    sync.RWMutex
	games map[GameKey]*game.GameContext
}

func New() *GameStateStore {
	return &GameStateStore{
		games: make(map[GameKey]*game.GameContext),
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
	s.games[GameKey{Sport: gc.Sport, GameID: gc.EID}] = gc
}

// Delete removes a game from the store and shuts down its goroutine.
func (s *GameStateStore) Delete(sport events.Sport, gameID string) {
	s.mu.Lock()
	key := GameKey{Sport: sport, GameID: gameID}
	gc, ok := s.games[key]
	delete(s.games, key)
	s.mu.Unlock()

	if ok {
		gc.Close()
	}
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

// BySport returns all game contexts for a given sport.
func (s *GameStateStore) BySport(sport events.Sport) []*game.GameContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*game.GameContext
	for k, gc := range s.games {
		if k.Sport == sport {
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
