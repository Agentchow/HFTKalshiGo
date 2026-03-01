package hockey

import (
	"fmt"
	"time"

	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Strategy implements the hockey-specific trading logic.
// Pregame odds are applied by the engine at initialization time —
// the strategy only handles live evaluation, model computation, and order generation.
type Strategy struct {
	lastPendingLog time.Time
}

func NewStrategy() *Strategy {
	return &Strategy{}
}

func (s *Strategy) Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) strategy.EvalResult {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return strategy.EvalResult{}
	}

	overturn := false
	if hs.HasLIVEData() {
		result := hs.CheckScoreDrop(gu.HomeScore, gu.AwayScore, 15)
		switch result {
		case "new_drop":
			telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
				gu.HomeTeam, gu.AwayTeam, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: hs.GetHomeScore(), OldAway: hs.GetAwayScore(),
				NewHome: gu.HomeScore, NewAway: gu.AwayScore,
			}
			gc.Notify(string(events.StatusOverturnPending))
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{}
		case "pending":
			if time.Since(s.lastPendingLog) >= 5*time.Second {
				telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
					gu.HomeTeam, gu.AwayTeam, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{}
		case "rejected":
			telemetry.Infof("[OVERTURN-REJECTED] %s vs %s (score restored to %d-%d)",
				gu.HomeTeam, gu.AwayTeam, gu.HomeScore, gu.AwayScore)
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: hs.GetHomeScore(), OldAway: hs.GetAwayScore(),
				NewHome: hs.RejectedHome, NewAway: hs.RejectedAway,
			}
			gc.Notify(string(events.StatusOverturnRejected))
		case "confirmed":
			overturn = true
			telemetry.Infof("[OVERTURN-CONFIRMED] %s vs %s (%d-%d -> %d-%d)",
				gu.HomeTeam, gu.AwayTeam, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			gc.LastOverturn = &game.OverturnInfo{
				OldHome: hs.GetHomeScore(), OldAway: hs.GetAwayScore(),
				NewHome: gu.HomeScore, NewAway: gu.AwayScore,
			}
			gc.Notify(string(events.StatusOverturnConfirmed))
		}
	}

	hadLIVEData := hs.HasLIVEData()
	changed := hs.UpdateGameState(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	s.updatePowerPlay(gc, hs, gu)

	s.computeModel(hs)
	hs.RecalcEdge(gc.Tickers)

	if hs.TimeLeft < 0.01 && hs.Lead() != 0 && !hs.IsFinished() && !hs.Finaled() {
		hs.SetFinaled()
		telemetry.Infof("[EARLY-SLAM] %s vs %s — clock 0:00, score %d-%d",
			hs.HomeTeam, hs.AwayTeam, hs.HomeScore, hs.AwayScore)
		return strategy.EvalResult{
			Intents:  s.slamOrders(gc, hs, gu),
			Finished: true,
		}
	}

	if !changed && !overturn {
		return strategy.EvalResult{}
	}

	scoreChanged := hadLIVEData && changed

	telemetry.Metrics.ScoreChanges.Inc()

	// Build orders if the score changed or overturn occurred, and there is a significant edge.
	if (scoreChanged || overturn) && hs.HasSignificantEdge() {
		return strategy.EvalResult{
			Intents: s.buildOrderIntents(gc, hs, overturn),
		}
	}

	return strategy.EvalResult{}
}

func (s *Strategy) OnFinish(gc *game.GameContext, gu *events.GameUpdateEvent) []events.OrderIntent {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return nil
	}

	s.computeModel(hs)
	return s.slamOrders(gc, hs, gu)
}

func (s *Strategy) computeModel(hs *hockeyState.HockeyState) {
	lead := float64(hs.Lead())
	if hs.IsOVERTIME() && lead != 0 {
		if lead > 0 {
			hs.ModelHomePct = 100.0
			hs.ModelAwayPct = 0.0
		} else {
			hs.ModelHomePct = 0.0
			hs.ModelAwayPct = 100.0
		}
		return
	}

	hs.ModelHomePct = ProjectedOdds(hs.HomeStrength, hs.TimeLeft, lead) * 100
	hs.ModelAwayPct = ProjectedOdds(hs.AwayStrength, hs.TimeLeft, -lead) * 100
}

func (s *Strategy) DisplayGame(gc *game.GameContext, eventType string) {
	display.PrintHockey(gc, eventType)
}

func (s *Strategy) OnPriceUpdate(gc *game.GameContext) []events.OrderIntent {
	return nil
}

func (s *Strategy) updatePowerPlay(gc *game.GameContext, hs *hockeyState.HockeyState, gu *events.GameUpdateEvent) {
	var homeOn, awayOn bool

	if gu.PowerPlay {
		homeDelta := gu.HomePenaltyCount - hs.HomePenaltyCount
		awayDelta := gu.AwayPenaltyCount - hs.AwayPenaltyCount

		switch {
		case awayDelta > homeDelta:
			homeOn = true
		case homeDelta > awayDelta:
			awayOn = true
		default:
			homeOn = hs.IsHomePowerPlay
			awayOn = hs.IsAwayPowerPlay
			if !homeOn && !awayOn {
				homeOn = true
			}
		}
	}

	hs.HomePenaltyCount = gu.HomePenaltyCount
	hs.AwayPenaltyCount = gu.AwayPenaltyCount

	if homeOn != hs.IsHomePowerPlay || awayOn != hs.IsAwayPowerPlay {
		if !homeOn && !awayOn {
			// PP just ended — capture who had it before clearing
			if hs.IsHomePowerPlay {
				t := true
				hs.LastPowerPlayWasHome = &t
			} else if hs.IsAwayPowerPlay {
				t := false
				hs.LastPowerPlayWasHome = &t
			}
		} else {
			hs.LastPowerPlayWasHome = nil
		}
		hs.IsHomePowerPlay = homeOn
		hs.IsAwayPowerPlay = awayOn
		if homeOn || awayOn {
			gc.Notify(string(events.StatusPowerPlay))
		} else {
			gc.Notify(string(events.StatusPowerPlayEnd))
		}
	}
}

// buildOrderIntents fires 4 orders covering all markets when a score change
// (or confirmed overturn) occurs and at least one significant edge exists.
func (s *Strategy) buildOrderIntents(gc *game.GameContext, hs *hockeyState.HockeyState, overturn bool) []events.OrderIntent {
	var intents []events.OrderIntent
	t := game.EdgeThresholdPct()

	if hs.HomeTicker != "" {
		intents = append(intents,
			events.OrderIntent{
				Sport: gc.Sport, League: gc.League, GameID: gc.EID, EID: gc.EID,
				Ticker: hs.HomeTicker, Side: "yes", Outcome: "home",
				LimitPct:  hs.ModelHomePct - t,
				Reason:    fmt.Sprintf("model %.1f%% YES", hs.ModelHomePct),
				HomeScore: hs.HomeScore, AwayScore: hs.AwayScore, Overturn: overturn,
			},
			events.OrderIntent{
				Sport: gc.Sport, League: gc.League, GameID: gc.EID, EID: gc.EID,
				Ticker: hs.HomeTicker, Side: "no", Outcome: "home",
				LimitPct:  (100 - hs.ModelHomePct) - t,
				Reason:    fmt.Sprintf("model %.1f%% NO", 100-hs.ModelHomePct),
				HomeScore: hs.HomeScore, AwayScore: hs.AwayScore, Overturn: overturn,
			},
		)
	}

	// Fade Orders
	if hs.AwayTicker != "" {
		intents = append(intents,
			events.OrderIntent{
				Sport: gc.Sport, League: gc.League, GameID: gc.EID, EID: gc.EID,
				Ticker: hs.AwayTicker, Side: "yes", Outcome: "away",
				LimitPct:  hs.ModelAwayPct - t,
				Reason:    fmt.Sprintf("model %.1f%% YES", hs.ModelAwayPct),
				HomeScore: hs.HomeScore, AwayScore: hs.AwayScore, Overturn: overturn,
			},
			events.OrderIntent{
				Sport: gc.Sport, League: gc.League, GameID: gc.EID, EID: gc.EID,
				Ticker: hs.AwayTicker, Side: "no", Outcome: "away",
				LimitPct:  (100 - hs.ModelAwayPct) - t,
				Reason:    fmt.Sprintf("model %.1f%% NO", 100-hs.ModelAwayPct),
				HomeScore: hs.HomeScore, AwayScore: hs.AwayScore, Overturn: overturn,
			},
		)
	}

	return intents
}

func (s *Strategy) slamOrders(gc *game.GameContext, hs *hockeyState.HockeyState, gu *events.GameUpdateEvent) []events.OrderIntent {
	if gu.HomeScore == gu.AwayScore {
		return nil
	}

	var winTicker, loseTicker, winOutcome, loseOutcome string
	if gu.HomeScore > gu.AwayScore {
		winTicker = hs.HomeTicker
		loseTicker = hs.AwayTicker
		winOutcome = "home"
		loseOutcome = "away"
	} else {
		winTicker = hs.AwayTicker
		loseTicker = hs.HomeTicker
		winOutcome = "away"
		loseOutcome = "home"
	}

	if winTicker == "" {
		return nil
	}

	reason := fmt.Sprintf("game finished %d-%d", gu.HomeScore, gu.AwayScore)

	intents := []events.OrderIntent{{
		Sport:     gu.Sport,
		League:    gu.League,
		GameID:    gu.EID,
		EID:       gu.EID,
		Ticker:    winTicker,
		Side:      "yes",
		Outcome:   winOutcome,
		LimitPct:  99,
		Reason:    reason,
		HomeScore: gu.HomeScore,
		AwayScore: gu.AwayScore,
		Slam:      true,
	}}

	if loseTicker != "" {
		intents = append(intents, events.OrderIntent{
			Sport:     gu.Sport,
			League:    gu.League,
			GameID:    gu.EID,
			EID:       gu.EID,
			Ticker:    loseTicker,
			Side:      "no",
			Outcome:   loseOutcome,
			LimitPct:  99,
			Reason:    reason,
			HomeScore: gu.HomeScore,
			AwayScore: gu.AwayScore,
			Slam:      true,
		})
	}

	return intents
}
