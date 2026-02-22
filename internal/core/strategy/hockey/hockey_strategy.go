package hockey

import (
	"fmt"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const discrepancyPct = 3.0 // minimum model-vs-Kalshi spread (%) to trigger an order

// Strategy implements the hockey-specific trading logic.
// On each score change it:
//  1. Updates the game state
//  2. Runs the score-drop guard
//  3. Recomputes model probabilities (Pinnacle first, math model fallback)
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

	// Compute model: Pinnacle first, math model fallback
	s.computeModel(hs)

	telemetry.Debugf("hockey: model %s  %s %.1f%% vs %s %.1f%%  score=%d-%d  period=%s  time=%.0fm  source=%s",
		sc.EID,
		hs.GetHomeTeam(), hs.ModelHomePct,
		hs.GetAwayTeam(), hs.ModelAwayPct,
		hs.GetHomeScore(), hs.GetAwayScore(),
		hs.GetPeriod(), hs.GetTimeRemaining(),
		s.modelSource(hs))

	return s.checkEdges(gc, hs, sc)
}

func (s *Strategy) OnFinish(gc *game.GameContext, gf *events.GameFinishEvent) []events.OrderIntent {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return nil
	}

	telemetry.Infof("hockey: game finished %s vs %s -> %d-%d (%s)",
		hs.GetAwayTeam(), hs.GetHomeTeam(), gf.AwayScore, gf.HomeScore, gf.FinalState)

	return s.slamOrders(gc, hs, gf)
}

// computeModel sets ModelHomePct and ModelAwayPct on the game state.
// Pinnacle odds are preferred when available; otherwise falls back
// to the projected_odds math model.
func (s *Strategy) computeModel(hs *hockeyState.HockeyState) {
	if hs.PinnacleHomePct != nil && hs.PinnacleAwayPct != nil {
		hs.ModelHomePct = *hs.PinnacleHomePct
		hs.ModelAwayPct = *hs.PinnacleAwayPct
		return
	}

	lead := float64(hs.Lead())
	if hs.IsOvertime() && lead != 0 {
		if lead > 0 {
			hs.ModelHomePct = 100.0
			hs.ModelAwayPct = 0.0
		} else {
			hs.ModelHomePct = 0.0
			hs.ModelAwayPct = 100.0
		}
		return
	}

	hs.ModelHomePct = ProjectedOdds(hs.HomeWinPct, hs.TimeLeft, lead) * 100
	hs.ModelAwayPct = ProjectedOdds(hs.AwayWinPct, hs.TimeLeft, -lead) * 100
}

func (s *Strategy) modelSource(hs *hockeyState.HockeyState) string {
	if hs.PinnacleHomePct != nil && hs.PinnacleAwayPct != nil {
		return "pinnacle"
	}
	return "math"
}

// checkEdges compares model probabilities against Kalshi market prices
// and returns OrderIntents for any edges above the discrepancy threshold.
func (s *Strategy) checkEdges(gc *game.GameContext, hs *hockeyState.HockeyState, sc *events.ScoreChangeEvent) []events.OrderIntent {
	var intents []events.OrderIntent

	for _, edge := range []struct {
		ticker   string
		outcome  string
		modelPct float64
	}{
		{hs.HomeTicker, "home", hs.ModelHomePct},
		{hs.AwayTicker, "away", hs.ModelAwayPct},
	} {
		if edge.ticker == "" {
			continue
		}
		td, ok := gc.Tickers[edge.ticker]
		if !ok || td.YesAsk <= 0 {
			continue
		}

		kalshiPct := td.YesAsk // yes_ask is already in cents (0-100)
		diff := edge.modelPct - kalshiPct
		if diff < discrepancyPct {
			continue
		}

		if hs.HasOrdered(edge.outcome) {
			continue
		}

		telemetry.Infof("hockey: edge %s %s  model=%.1f%%  kalshi=%.0f¢  diff=+%.1f%%",
			sc.EID, edge.outcome, edge.modelPct, kalshiPct, diff)

		hs.MarkOrdered(edge.outcome)
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

// slamOrders emits high-confidence orders when a game finishes with a clear winner.
func (s *Strategy) slamOrders(gc *game.GameContext, hs *hockeyState.HockeyState, gf *events.GameFinishEvent) []events.OrderIntent {
	if gf.HomeScore == gf.AwayScore {
		return nil
	}

	var winTicker, outcome string
	if gf.HomeScore > gf.AwayScore {
		winTicker = hs.HomeTicker
		outcome = "home"
	} else {
		winTicker = hs.AwayTicker
		outcome = "away"
	}

	if winTicker == "" {
		return nil
	}

	telemetry.Infof("hockey: slam %s  winner=%s  %d-%d",
		gf.EID, outcome, gf.HomeScore, gf.AwayScore)

	return []events.OrderIntent{{
		Sport:     gf.Sport,
		League:    gf.League,
		GameID:    gf.EID,
		EID:       gf.EID,
		Ticker:    winTicker,
		Side:      "yes",
		Outcome:   outcome,
		LimitPct:  99,
		Reason:    fmt.Sprintf("game finished %d-%d", gf.HomeScore, gf.AwayScore),
		HomeScore: gf.HomeScore,
		AwayScore: gf.AwayScore,
	}}
}
