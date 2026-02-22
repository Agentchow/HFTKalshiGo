package soccer

import (
	"fmt"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	parentStrategy "github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	discrepancyPct = 3.0  // base edge threshold (%)
	rampSec        = 300.0 // ramp window before game end (seconds)
)

// Strategy implements soccer-specific 3-way (1X2) trading logic.
type Strategy struct {
	scoreDropConfirmSec int
	lamCache            map[string][2]float64 // EID → (lamHome, lamAway)
}

func NewStrategy(scoreDropConfirmSec int) *Strategy {
	return &Strategy{
		scoreDropConfirmSec: scoreDropConfirmSec,
		lamCache:            make(map[string][2]float64),
	}
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
			ss.ClearOrdered()
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

	s.computeModel(ss)

	telemetry.Infof("soccer: model %s  H=%.1f%% D=%.1f%% A=%.1f%%  score=%d-%d  half=%s  time=%.0fm  source=%s",
		sc.EID,
		ss.ModelHomeYes, ss.ModelDrawYes, ss.ModelAwayYes,
		ss.GetHomeScore(), ss.GetAwayScore(),
		ss.GetPeriod(), ss.GetTimeRemaining(),
		s.modelSource(ss))

	return s.checkEdges(gc, ss, sc)
}

func (s *Strategy) OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return nil
	}

	telemetry.Infof("soccer: match finished %s vs %s -> %d-%d (%s)",
		ss.GetHomeTeam(), ss.GetAwayTeam(), gf.HomeScore, gf.AwayScore, gf.FinalState)

	return s.slamOrders(gc, ss, gf)
}

func (s *Strategy) computeModel(ss *soccerState.SoccerState) {
	if ss.PinnacleHomePct != nil && ss.PinnacleDrawPct != nil && ss.PinnacleAwayPct != nil {
		ss.ModelHomeYes = *ss.PinnacleHomePct
		ss.ModelDrawYes = *ss.PinnacleDrawPct
		ss.ModelAwayYes = *ss.PinnacleAwayPct
		ss.ModelHomeNo = 100 - ss.ModelHomeYes
		ss.ModelDrawNo = 100 - ss.ModelDrawYes
		ss.ModelAwayNo = 100 - ss.ModelAwayYes
		return
	}

	lamH, lamA := s.getLambdas(ss)
	redsH, redsA := ss.RedCardCounts()

	goalDiff := ss.GoalDiff()
	if ss.ExtraTimeSettlesML && ss.IsRegulationOver() {
		goalDiff = ss.RegulationGoalDiff()
	}

	probs := InplayProbs(lamH, lamA, ss.TimeLeft, goalDiff, ss.HalfNumber(), redsH, redsA, ss.IsLive())

	ss.ModelHomeYes = probs.Home * 100
	ss.ModelDrawYes = probs.Draw * 100
	ss.ModelAwayYes = probs.Away * 100
	ss.ModelHomeNo = 100 - ss.ModelHomeYes
	ss.ModelDrawNo = 100 - ss.ModelDrawYes
	ss.ModelAwayNo = 100 - ss.ModelAwayYes
}

func (s *Strategy) getLambdas(ss *soccerState.SoccerState) (float64, float64) {
	if cached, ok := s.lamCache[ss.EID]; ok {
		return cached[0], cached[1]
	}
	lH, lA := InferLambdas(ss.HomeWinPct, ss.DrawPct, ss.AwayWinPct, ss.G0)
	s.lamCache[ss.EID] = [2]float64{lH, lA}
	return lH, lA
}

func (s *Strategy) modelSource(ss *soccerState.SoccerState) string {
	if ss.PinnacleHomePct != nil && ss.PinnacleDrawPct != nil && ss.PinnacleAwayPct != nil {
		return "pinnacle"
	}
	return "poisson"
}

func (s *Strategy) checkEdges(gc *game.GameContext, ss *soccerState.SoccerState, sc *events.ScoreChangeEvent) []events.OrderIntent {
	threshold := parentStrategy.EffectiveThreshold(ss.TimeLeft, discrepancyPct, rampSec)

	var intents []events.OrderIntent
	for _, edge := range []struct {
		ticker   string
		outcome  string
		modelPct float64
	}{
		{ss.HomeTicker, "home", ss.ModelHomeYes},
		{ss.DrawTicker, "draw", ss.ModelDrawYes},
		{ss.AwayTicker, "away", ss.ModelAwayYes},
	} {
		if edge.ticker == "" {
			continue
		}
		td, ok := gc.Tickers[edge.ticker]
		if !ok || td.YesAsk <= 0 {
			continue
		}

		kalshiPct := td.YesAsk
		diff := edge.modelPct - kalshiPct
		if diff < threshold {
			continue
		}

		if ss.HasOrdered(edge.outcome) {
			continue
		}

		telemetry.Infof("soccer: edge %s %s  model=%.1f%%  kalshi=%.0f¢  diff=+%.1f%%  threshold=%.1f%%",
			sc.EID, edge.outcome, edge.modelPct, kalshiPct, diff, threshold)

		ss.MarkOrdered(edge.outcome)
		intents = append(intents, events.OrderIntent{
			Sport:     sc.Sport,
			League:    sc.League,
			GameID:    sc.EID,
			EID:       sc.EID,
			Ticker:    edge.ticker,
			Side:      "yes",
			Outcome:   edge.outcome,
			LimitPct:  edge.modelPct,
			Reason:    fmt.Sprintf("model %.1f%% vs kalshi %.0f¢ (+%.1f%%)", edge.modelPct, kalshiPct, diff),
			HomeScore: sc.HomeScore,
			AwayScore: sc.AwayScore,
		})
	}

	return intents
}

func (s *Strategy) slamOrders(gc *game.GameContext, ss *soccerState.SoccerState, gf *events.GameFinishEvent) []events.OrderIntent {
	var intents []events.OrderIntent

	goalDiff := gf.HomeScore - gf.AwayScore
	if ss.ExtraTimeSettlesML && ss.IsRegulationOver() {
		goalDiff = ss.RegulationGoalDiff()
	}

	type slam struct {
		ticker  string
		outcome string
		won     bool
	}
	slams := []slam{
		{ss.HomeTicker, "home", goalDiff > 0},
		{ss.DrawTicker, "draw", goalDiff == 0},
		{ss.AwayTicker, "away", goalDiff < 0},
	}

	for _, sl := range slams {
		if sl.ticker == "" || !sl.won {
			continue
		}

		telemetry.Infof("soccer: slam %s  winner=%s  %d-%d", gf.EID, sl.outcome, gf.HomeScore, gf.AwayScore)

		intents = append(intents, events.OrderIntent{
			Sport:     gf.Sport,
			League:    gf.League,
			GameID:    gf.EID,
			EID:       gf.EID,
			Ticker:    sl.ticker,
			Side:      "yes",
			Outcome:   sl.outcome,
			LimitPct:  99,
			Reason:    fmt.Sprintf("match finished %d-%d", gf.HomeScore, gf.AwayScore),
			HomeScore: gf.HomeScore,
			AwayScore: gf.AwayScore,
		})
	}

	return intents
}
