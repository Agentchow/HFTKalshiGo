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

// Clear resets all dedup state (e.g. after a confirmed score overturn).
func (g *IdempotencyGuard) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seen = make(map[string]bool)
}
