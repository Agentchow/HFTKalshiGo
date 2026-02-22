package soccer

import (
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Strategy implements soccer-specific 3-way (1X2) trading logic.
type Strategy struct {
	scoreDropConfirmSec int
	lastPendingLog      time.Time
}

func NewStrategy(scoreDropConfirmSec int) *Strategy {
	return &Strategy{scoreDropConfirmSec: scoreDropConfirmSec}
}

func (s *Strategy) Evaluate(gc *game.GameContext, sc *events.ScoreChangeEvent) []events.OrderIntent {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return nil
	}

	tracked := len(gc.Tickers) > 0

	if ss.HasLiveData() {
		result := ss.CheckScoreDrop(sc.HomeScore, sc.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "new_drop":
			if tracked {
				telemetry.Infof("soccer: score drop %s for %s @ %s [%s] (%d-%d -> %d-%d)",
					result, ss.AwayTeam, ss.HomeTeam, sc.EID,
					ss.GetHomeScore(), ss.GetAwayScore(), sc.HomeScore, sc.AwayScore)
			}
			s.lastPendingLog = time.Now()
			return nil
		case "pending":
			if tracked && time.Since(s.lastPendingLog) >= 20*time.Second {
				telemetry.Infof("soccer: score drop %s for %s @ %s [%s] (%d-%d -> %d-%d)",
					result, ss.AwayTeam, ss.HomeTeam, sc.EID,
					ss.GetHomeScore(), ss.GetAwayScore(), sc.HomeScore, sc.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return nil
		case "confirmed":
			if tracked {
				telemetry.Infof("soccer: overturn confirmed for %s @ %s [%s] -> %d-%d",
					ss.AwayTeam, ss.HomeTeam, sc.EID, sc.HomeScore, sc.AwayScore)
			}
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

	if sc.HomeRedCards > 0 || sc.AwayRedCards > 0 {
		ss.UpdateRedCards(sc.HomeRedCards, sc.AwayRedCards)
	}

	// TODO: plug in Poisson model and 6-way edge evaluation
	return nil
}

func (s *Strategy) OnPriceUpdate(gc *game.GameContext) []events.OrderIntent {
	return nil
}

func (s *Strategy) OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return nil
	}

	_ = ss
	// TODO: emit slam orders for settled 1X2 markets
	return nil
}
