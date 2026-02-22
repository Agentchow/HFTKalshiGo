package football

import (
	"github.com/charleschow/hft-trading/internal/core/state/game"
	fbState "github.com/charleschow/hft-trading/internal/core/state/game/football"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Strategy implements American football trading logic.
type Strategy struct {
	scoreDropConfirmSec int
}

func NewStrategy(scoreDropConfirmSec int) *Strategy {
	return &Strategy{scoreDropConfirmSec: scoreDropConfirmSec}
}

func (s *Strategy) Evaluate(gc *game.GameContext, sc *events.ScoreChangeEvent) []events.OrderIntent {
	fs, ok := gc.Game.(*fbState.FootballState)
	if !ok {
		return nil
	}

	if fs.HasLiveData() {
		result := fs.CheckScoreDrop(sc.HomeScore, sc.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "pending", "new_drop":
			telemetry.Infof("football: score drop %s for %s (%d-%d -> %d-%d)",
				result, sc.EID, fs.GetHomeScore(), fs.GetAwayScore(), sc.HomeScore, sc.AwayScore)
			return nil
		case "confirmed":
			telemetry.Infof("football: overturn confirmed for %s -> %d-%d",
				sc.EID, sc.HomeScore, sc.AwayScore)
		}
	}

	changed := fs.UpdateScore(sc.HomeScore, sc.AwayScore, sc.Period, sc.TimeLeft)
	if !changed {
		return nil
	}

	telemetry.Metrics.ScoreChanges.Inc()

	if sc.HomeWinPct != nil && sc.AwayWinPct != nil {
		h := *sc.HomeWinPct * 100
		a := *sc.AwayWinPct * 100
		fs.ModelHomePct = h
		fs.ModelAwayPct = a
	}

	// TODO: plug in football model and edge detection
	return nil
}

func (s *Strategy) OnPriceUpdate(gc *game.GameContext) []events.OrderIntent {
	return nil
}

func (s *Strategy) OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent {
	fs, ok := gc.Game.(*fbState.FootballState)
	if !ok {
		return nil
	}

	_ = fs
	// TODO: emit slam orders for settled markets
	return nil
}
