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
	scoreDropConfirmSec int
	lastPendingLog      time.Time
}

func NewStrategy(scoreDropConfirmSec int) *Strategy {
	return &Strategy{scoreDropConfirmSec: scoreDropConfirmSec}
}

func (s *Strategy) Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) strategy.EvalResult {
	fs, ok := gc.Game.(*fbState.FootballState)
	if !ok {
		return strategy.EvalResult{}
	}

	if fs.HasLiveData() {
		result := fs.CheckScoreDrop(gu.HomeScore, gu.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "new_drop":
			telemetry.Infof("football: score drop %s for %s (%d-%d -> %d-%d)",
				result, gu.EID, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{}
		case "pending":
			if time.Since(s.lastPendingLog) >= 20*time.Second {
				telemetry.Infof("football: score drop %s for %s (%d-%d -> %d-%d)",
					result, gu.EID, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{}
		case "confirmed":
			telemetry.Infof("football: overturn confirmed for %s -> %d-%d",
				gu.EID, gu.HomeScore, gu.AwayScore)
		}
	}

	changed := fs.UpdateScore(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	if !changed {
		return strategy.EvalResult{}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	if gu.HomeWinPct != nil && gu.AwayWinPct != nil {
		h := *gu.HomeWinPct * 100
		a := *gu.AwayWinPct * 100
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
