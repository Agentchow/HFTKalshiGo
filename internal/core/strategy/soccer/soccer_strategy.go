package soccer

import (
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const edgeThreshold = 3.0 // percentage points

// Strategy implements soccer-specific 3-way (1X2) trading logic.
// Pregame odds are applied by the engine at initialization time —
// the strategy only handles live evaluation.
type Strategy struct {
	lastPendingLog time.Time
}

func NewStrategy() *Strategy {
	return &Strategy{}
}

func (s *Strategy) Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) strategy.EvalResult {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return strategy.EvalResult{}
	}

	tracked := len(gc.Tickers) > 0

	if ss.HasLIVEData() {
		result := ss.CheckScoreDrop(gu.HomeScore, gu.AwayScore, 15)
		switch result {
		case "new_drop":
			if tracked {
				telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
					ss.HomeTeam, ss.AwayTeam,
					ss.GetHomeScore(), ss.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			}
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: ss.GetHomeScore(), OldAway: ss.GetAwayScore(),
				NewHome: gu.HomeScore, NewAway: gu.AwayScore,
			}
			gc.Notify(string(events.StatusOverturnPending))
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{}
		case "pending":
			if tracked && time.Since(s.lastPendingLog) >= 5*time.Second {
				telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
					ss.HomeTeam, ss.AwayTeam,
					ss.GetHomeScore(), ss.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{}
		case "rejected":
			if tracked {
				telemetry.Infof("[OVERTURN-REJECTED] %s vs %s (score restored to %d-%d)",
					ss.HomeTeam, ss.AwayTeam, gu.HomeScore, gu.AwayScore)
			}
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: ss.GetHomeScore(), OldAway: ss.GetAwayScore(),
				NewHome: ss.RejectedHome, NewAway: ss.RejectedAway,
			}
			gc.Notify(string(events.StatusOverturnRejected))
		case "confirmed":
			if tracked {
				telemetry.Infof("[OVERTURN-CONFIRMED] %s vs %s (%d-%d -> %d-%d)",
					ss.HomeTeam, ss.AwayTeam, ss.GetHomeScore(), ss.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			}
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: ss.GetHomeScore(), OldAway: ss.GetAwayScore(),
				NewHome: gu.HomeScore, NewAway: gu.AwayScore,
			}
			gc.Notify(string(events.StatusOverturnConfirmed))
		}
	}

	changed := ss.UpdateGameState(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	prevHomeRC, prevAwayRC := ss.HomeRedCards, ss.AwayRedCards
	if gu.HomeRedCards > 0 || gu.AwayRedCards > 0 {
		ss.UpdateRedCards(gu.HomeRedCards, gu.AwayRedCards)
	}
	rcChanged := ss.HomeRedCards != prevHomeRC || ss.AwayRedCards != prevAwayRC
	if rcChanged {
		gc.Notify(string(events.StatusRedCard))
	}
	if !changed {
		return strategy.EvalResult{}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	ss.Bet365Updated = false
	if gu.LiveOddsHome != nil && gu.LiveOddsDraw != nil && gu.LiveOddsAway != nil {
		h := *gu.LiveOddsHome * 100
		d := *gu.LiveOddsDraw * 100
		a := *gu.LiveOddsAway * 100
		ss.Bet365Updated = ss.Bet365HomePct == nil || *ss.Bet365HomePct != h
		ss.Bet365HomePct = &h
		ss.Bet365DrawPct = &d
		ss.Bet365AwayPct = &a

		// TEMP: use Bet365 as model (edge = Bet365 - Kalshi)
		ss.ModelHomeYes = h
		ss.ModelDrawYes = d
		ss.ModelAwayYes = a
		ss.ModelHomeNo = 100 - h
		ss.ModelDrawNo = 100 - d
		ss.ModelAwayNo = 100 - a
	}

	return strategy.EvalResult{}
}

func (s *Strategy) OnPriceUpdate(gc *game.GameContext) []events.OrderIntent {
	return nil
}

func (s *Strategy) OnFinish(gc *game.GameContext, gu *events.GameUpdateEvent) []events.OrderIntent {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return nil
	}

	_ = ss
	// TODO: emit slam orders for settled 1X2 markets
	return nil
}

func (s *Strategy) DisplayGame(gc *game.GameContext, eventType string) {
	display.PrintSoccer(gc, eventType)
}
