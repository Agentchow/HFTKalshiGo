package football

import (
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	fbState "github.com/charleschow/hft-trading/internal/core/state/game/football"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Strategy implements American football trading logic.
type Strategy struct {
	lastPendingLog time.Time
}

func NewStrategy() *Strategy {
	return &Strategy{}
}

func (s *Strategy) Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) strategy.EvalResult {
	fs, ok := gc.Game.(*fbState.FootballState)
	if !ok {
		return strategy.EvalResult{}
	}

	if fs.HasLIVEData() {
		result := fs.CheckScoreDrop(gu.HomeScore, gu.AwayScore, 15)
		switch result {
		case "new_drop":
			telemetry.Infof("football: score drop %s for %s (%d-%d -> %d-%d)",
				result, gu.EID, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{}
		case "pending":
			if time.Since(s.lastPendingLog) >= 5*time.Second {
				telemetry.Infof("football: score drop %s for %s (%d-%d -> %d-%d)",
					result, gu.EID, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{}
		case "rejected":
			telemetry.Infof("[OVERTURN-REJECTED] %s (score restored to %d-%d)",
				gu.EID, gu.HomeScore, gu.AwayScore)
		case "confirmed":
			telemetry.Infof("[OVERTURN-CONFIRMED] %s (%d-%d -> %d-%d)",
				gu.EID, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
		}
	}

	changed := fs.UpdateGameState(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	if !changed {
		return strategy.EvalResult{}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	if gu.HomeStrength != nil && gu.AwayStrength != nil {
		h := *gu.HomeStrength * 100
		a := *gu.AwayStrength * 100
		fs.ModelHomePct = h
		fs.ModelAwayPct = a
	}

	// TODO: plug in football model and edge detection
	return strategy.EvalResult{}
}

func (s *Strategy) HasSignificantEdge(gc *game.GameContext) bool { return false }

func (s *Strategy) OnPriceUpdate(gc *game.GameContext) []events.OrderIntent {
	return nil
}

func (s *Strategy) OnFinish(gc *game.GameContext, gu *events.GameUpdateEvent) []events.OrderIntent {
	fs, ok := gc.Game.(*fbState.FootballState)
	if !ok {
		return nil
	}

	_ = fs
	// TODO: emit slam orders for settled markets
	return nil
}
