package lanes

import (
	"fmt"
	"sync"
)

// IdempotencyGuard prevents duplicate orders for the same
// (ticker, home_score, away_score) tuple within a lane.
type IdempotencyGuard struct {
	mu   sync.RWMutex
	seen map[string]bool
}

func NewIdempotencyGuard() *IdempotencyGuard {
	return &IdempotencyGuard{
		seen: make(map[string]bool),
	}
}

// Key builds a dedup key from ticker and current score.
func (g *IdempotencyGuard) Key(ticker string, homeScore, awayScore int) string {
	return fmt.Sprintf("%s:%d-%d", ticker, homeScore, awayScore)
}

func (g *IdempotencyGuard) HasSeen(key string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.seen[key]
}

func (g *IdempotencyGuard) Record(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen[key] = true
}

// Clear resets all dedup state.
func (g *IdempotencyGuard) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen = make(map[string]bool)
}

// ClearForTicker removes all dedup entries for a specific ticker
// (all score combinations), used after a confirmed score overturn.
func (g *IdempotencyGuard) ClearForTicker(ticker string) {
	prefix := ticker + ":"
	g.mu.Lock()
	defer g.mu.Unlock()
	for k := range g.seen {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(g.seen, k)
		}
	}
}
