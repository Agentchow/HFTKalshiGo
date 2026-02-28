package execution

import (
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/events"
)

// RegisterLanesFromConfig reads sport/league risk limits from the loaded
// config and registers the appropriate execution lanes on the router.
func RegisterLanesFromConfig(router *LaneRouter, rl config.RiskLimits, sport events.Sport, sportKey string) {
	sl, ok := rl.SportLimit(sportKey)
	if !ok {
		RegisterSportLanes(router, 50000, nil, sport)
		return
	}
	leagues := make(map[string]int, len(sl.Leagues))
	for league, ll := range sl.Leagues {
		leagues[league] = ll.MaxGameCents
	}
	RegisterSportLanes(router, sl.MaxSportCents, leagues, sport)
	if sl.OrderTTLSeconds > 0 {
		router.SetOrderTTL(sport, sl.OrderTTLSeconds)
	}
}
