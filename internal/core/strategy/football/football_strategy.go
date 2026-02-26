package football

import (
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
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
			telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
				gu.HomeTeam, gu.AwayTeam, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: fs.GetHomeScore(), OldAway: fs.GetAwayScore(),
				NewHome: gu.HomeScore, NewAway: gu.AwayScore,
			}
			gc.Notify(string(events.StatusOverturnPending))
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{}
		case "pending":
			if time.Since(s.lastPendingLog) >= 5*time.Second {
				telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
					gu.HomeTeam, gu.AwayTeam, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{}
		case "rejected":
			telemetry.Infof("[OVERTURN-REJECTED] %s vs %s (score restored to %d-%d)",
				gu.HomeTeam, gu.AwayTeam, gu.HomeScore, gu.AwayScore)
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: fs.GetHomeScore(), OldAway: fs.GetAwayScore(),
				NewHome: fs.RejectedHome, NewAway: fs.RejectedAway,
			}
			gc.Notify(string(events.StatusOverturnRejected))
		case "confirmed":
			telemetry.Infof("[OVERTURN-CONFIRMED] %s vs %s (%d-%d -> %d-%d)",
				gu.HomeTeam, gu.AwayTeam, fs.GetHomeScore(), fs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: fs.GetHomeScore(), OldAway: fs.GetAwayScore(),
				NewHome: gu.HomeScore, NewAway: gu.AwayScore,
			}
			gc.Notify(string(events.StatusOverturnConfirmed))
		}
	}

	changed := fs.UpdateGameState(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	if !changed {
		return strategy.EvalResult{}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	// TODO: plug in football model and edge detection
	return strategy.EvalResult{}
}

func (s *Strategy) DisplayGame(gc *game.GameContext, eventType string) {
	display.PrintFootball(gc, eventType)
}

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
