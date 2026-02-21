package soccer

import (
	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Strategy implements soccer-specific 3-way (1X2) trading logic.
type Strategy struct {
	scoreDropConfirmSec int
}

func NewStrategy(scoreDropConfirmSec int) *Strategy {
	return &Strategy{scoreDropConfirmSec: scoreDropConfirmSec}
}

func (s *Strategy) Evaluate(gc *game.GameContext, sc *events.ScoreChangeEvent) []events.OrderIntent {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return nil
	}

	if ss.HasLiveData() {
		result := ss.CheckScoreDrop(sc.HomeScore, sc.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "pending", "new_drop":
			telemetry.Infof("soccer: score drop %s for %s (%d-%d -> %d-%d)",
				result, sc.EID, ss.GetHomeScore(), ss.GetAwayScore(), sc.HomeScore, sc.AwayScore)
			return nil
		case "confirmed":
			telemetry.Infof("soccer: overturn confirmed for %s -> %d-%d",
				sc.EID, sc.HomeScore, sc.AwayScore)
		}
	}

	changed := ss.UpdateScore(sc.HomeScore, sc.AwayScore, sc.Period, sc.TimeLeft)
	if !changed {
		return nil
	}

	telemetry.Metrics.ScoreChanges.Inc()

	if sc.HomeWinPct != nil && sc.DrawPct != nil && sc.AwayWinPct != nil {
		h := *sc.HomeWinPct * 100
		d := *sc.DrawPct * 100
		a := *sc.AwayWinPct * 100
		ss.PinnacleHomePct = &h
		ss.PinnacleDrawPct = &d
		ss.PinnacleAwayPct = &a
	}

	// TODO: plug in Poisson model and 6-way edge evaluation
	return nil
}

func (s *Strategy) OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return nil
	}

	telemetry.Infof("soccer: match finished %s vs %s -> %d-%d (%s)",
		ss.GetAwayTeam(), ss.GetHomeTeam(), gf.AwayScore, gf.HomeScore, gf.FinalState)

	// TODO: emit slam orders for settled 1X2 markets
	return nil
}
