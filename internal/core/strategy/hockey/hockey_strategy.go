package hockey

import (
	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Strategy implements the hockey-specific trading logic.
// On each score change it:
//  1. Updates the game state
//  2. Runs the score-drop guard
//  3. Recomputes model probabilities
//  4. Compares model vs market and emits OrderIntents for edges
type Strategy struct {
	scoreDropConfirmSec int
}

func NewStrategy(scoreDropConfirmSec int) *Strategy {
	return &Strategy{scoreDropConfirmSec: scoreDropConfirmSec}
}

func (s *Strategy) Evaluate(gc *game.GameContext, sc *events.ScoreChangeEvent) []events.OrderIntent {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return nil
	}

	// Score-drop guard
	if hs.HasLiveData() {
		result := hs.CheckScoreDrop(sc.HomeScore, sc.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "pending", "new_drop":
			telemetry.Infof("hockey: score drop %s for %s (%d-%d -> %d-%d)",
				result, sc.EID, hs.GetHomeScore(), hs.GetAwayScore(), sc.HomeScore, sc.AwayScore)
			return nil
		case "confirmed":
			hs.ClearOrdered()
			telemetry.Infof("hockey: overturn confirmed for %s -> %d-%d",
				sc.EID, sc.HomeScore, sc.AwayScore)
		}
	}

	changed := hs.UpdateScore(sc.HomeScore, sc.AwayScore, sc.Period, sc.TimeLeft)
	if !changed {
		return nil
	}

	telemetry.Metrics.ScoreChanges.Inc()

	// Update Pinnacle odds if available
	if sc.HomeWinPct != nil && sc.AwayWinPct != nil {
		h := *sc.HomeWinPct * 100
		a := *sc.AwayWinPct * 100
		hs.PinnacleHomePct = &h
		hs.PinnacleAwayPct = &a
	}

	// TODO: plug in actual model computation and edge detection
	// For now, return no intents — strategy logic to be implemented
	return nil
}

func (s *Strategy) OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return nil
	}

	telemetry.Infof("hockey: game finished %s vs %s -> %d-%d (%s)",
		hs.GetAwayTeam(), hs.GetHomeTeam(), gf.AwayScore, gf.HomeScore, gf.FinalState)

	// TODO: emit slam orders for settled markets
	return nil
}
