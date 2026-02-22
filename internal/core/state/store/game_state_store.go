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
	mu          sync.RWMutex
	games       map[GameKey]*game.GameContext
	tickerIndex map[string][]*game.GameContext
}

func New() *GameStateStore {
	return &GameStateStore{
		games:       make(map[GameKey]*game.GameContext),
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
	s.games[GameKey{Sport: gc.Sport, GameID: gc.EID}] = gc
}

// Delete removes a game from the store and shuts down its goroutine.
func (s *GameStateStore) Delete(sport events.Sport, gameID string) {
	s.mu.Lock()
	key := GameKey{Sport: sport, GameID: gameID}
	gc, ok := s.games[key]
	delete(s.games, key)
	if ok {
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
