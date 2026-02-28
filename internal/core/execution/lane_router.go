package execution

import (
	"fmt"
	"sync"

	"github.com/charleschow/hft-trading/internal/core/execution/lanes"
	"github.com/charleschow/hft-trading/internal/events"
)

// LaneRouter maps (sport, league) to a dedicated execution lane.
// Each lane has its own risk limits and idempotency state.
const defaultOrderTTL = 60

type LaneRouter struct {
	mu       sync.RWMutex
	lanes    map[string]*lanes.Lane  // "hockey:ahl" -> Lane
	sportTTL map[events.Sport]int    // sport -> order TTL in seconds
}

func NewLaneRouter() *LaneRouter {
	return &LaneRouter{
		lanes:    make(map[string]*lanes.Lane),
		sportTTL: make(map[events.Sport]int),
	}
}

// SetOrderTTL records the order time-to-live for a sport.
func (lr *LaneRouter) SetOrderTTL(sport events.Sport, seconds int) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.sportTTL[sport] = seconds
}

// OrderTTL returns the order TTL for a sport in seconds (default 60).
func (lr *LaneRouter) OrderTTL(sport events.Sport) int {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	if ttl, ok := lr.sportTTL[sport]; ok && ttl > 0 {
		return ttl
	}
	return defaultOrderTTL
}

func laneKey(sport events.Sport, league string) string {
	return fmt.Sprintf("%s:%s", sport, league)
}

func (lr *LaneRouter) Register(sport events.Sport, league string, lane *lanes.Lane) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.lanes[laneKey(sport, league)] = lane
}

func (lr *LaneRouter) Route(sport events.Sport, league string) *lanes.Lane {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	if lane, ok := lr.lanes[laneKey(sport, league)]; ok {
		return lane
	}
	// Fallback: try sport-level lane (e.g. "hockey:*")
	if lane, ok := lr.lanes[fmt.Sprintf("%s:*", sport)]; ok {
		return lane
	}
	return nil
}

// RegisterSportLanes wires risk limits for a single sport into the router.
func RegisterSportLanes(router *LaneRouter, maxSportCents int, leagueLimits map[string]int, sport events.Sport) {
	sportSpend := lanes.NewSpendGuard(maxSportCents)

	if len(leagueLimits) == 0 {
		router.Register(sport, "*", lanes.NewLaneWithSpend(5000, sportSpend))
		return
	}

	for league, maxGameCents := range leagueLimits {
		router.Register(sport, league, lanes.NewLaneWithSpend(maxGameCents, sportSpend))
	}
	router.Register(sport, "*", lanes.NewLaneWithSpend(5000, sportSpend))
}

