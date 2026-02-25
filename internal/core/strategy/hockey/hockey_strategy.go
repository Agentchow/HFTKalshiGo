package hockey

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/core/odds"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	discrepancyPct   = 3.0 // minimum model-vs-Kalshi spread (%) to trigger an order
	pregameCacheTTL  = 30 * time.Minute
	pregameRetryCool = 30 * time.Second
)

// PregameOddsProvider abstracts fetching pregame odds.
// Satisfied by *goalserve_http.PregameClient.
type PregameOddsProvider interface {
	FetchHockeyPregame() ([]odds.PregameOdds, error)
}

// Strategy implements the hockey-specific trading logic.
// On each SCORE CHANGE it:
//  1. Updates the game state
//  2. Runs the score-drop guard
//  3. Recomputes model probabilities (pregame strength + projected odds)
//  4. Compares model vs Kalshi market and emits OrderIntents for edges
type Strategy struct {
	lastPendingLog time.Time

	pregame        PregameOddsProvider
	pregameMu      sync.RWMutex
	pregameCache   []odds.PregameOdds
	pregameFetch   time.Time
	pregameLastTry time.Time
	pregameApplied sync.Map
}

func NewStrategy(pregame PregameOddsProvider) *Strategy {
	s := &Strategy{
		pregame: pregame,
	}
	if pregame != nil {
		s.loadPregameWithRetry()
	}
	return s
}

const (
	startupMaxAttempts = 5
	startupRetryDelay  = 15 * time.Second
)

func (s *Strategy) loadPregameWithRetry() {
	for attempt := 1; attempt <= startupMaxAttempts; attempt++ {
		fetched, err := s.pregame.FetchHockeyPregame()
		if err != nil {
			telemetry.Warnf("pregame: startup fetch attempt %d/%d failed: %v", attempt, startupMaxAttempts, err)
			if attempt < startupMaxAttempts {
				time.Sleep(startupRetryDelay)
			}
			continue
		}
		if fetched == nil {
			fetched = []odds.PregameOdds{}
		}
		s.pregameCache = fetched
		s.pregameFetch = time.Now()
		telemetry.Infof("pregame: loaded %d hockey matches on startup", len(fetched))
		return
	}
	telemetry.Errorf("pregame: all %d startup attempts failed — proceeding without pregame data", startupMaxAttempts)
}

func (s *Strategy) Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) strategy.EvalResult {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return strategy.EvalResult{}
	}

	if s.pregame != nil {
		if _, applied := s.pregameApplied.Load(gc.EID); !applied {
			if s.applyPregame(hs, gu.HomeTeam, gu.AwayTeam) {
				hs.PregameApplied = true
				s.pregameApplied.Store(gc.EID, true)
			}
		}
	}

	s.updatePowerPlay(gc, hs, gu)

	// Score-drop guard
	overturn := false
	if hs.HasLIVEData() {
		result := hs.CheckScoreDrop(gu.HomeScore, gu.AwayScore, 15)
		switch result {
		case "new_drop":
			telemetry.Infof("[OVERTURN-PENDING] %s vs %s (%d-%d -> %d-%d)",
				gu.HomeTeam, gu.AwayTeam, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
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
		case "confirmed":
			overturn = true
			telemetry.Infof("[OVERTURN-CONFIRMED] %s vs %s (%d-%d -> %d-%d)",
				gu.HomeTeam, gu.AwayTeam, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
		}
	}

	changed := hs.UpdateGameState(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	if !changed && !overturn {
		return strategy.EvalResult{}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	hs.Bet365Updated = false
	if gu.HomeStrength != nil && gu.AwayStrength != nil {
		h := *gu.HomeStrength * 100
		a := *gu.AwayStrength * 100
		hs.Bet365Updated = hs.Bet365HomePct == nil || *hs.Bet365HomePct != h
		hs.Bet365HomePct = &h
		hs.Bet365AwayPct = &a
	}

	s.computeModel(hs)
	hs.RecalcEdge(gc.Tickers)

	return strategy.EvalResult{
		Intents: s.buildOrderIntent(gc, hs, s.findEdges(hs), overturn),
	}
}

func (s *Strategy) OnFinish(gc *game.GameContext, gu *events.GameUpdateEvent) []events.OrderIntent {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return nil
	}

	return s.slamOrders(gc, hs, gu)
}

// computeModel sets ModelHomePct and ModelAwayPct using the pregame-strength
// math model. Bet365 odds are stored separately for display but not used
// in the model or edge calculations.
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

func (s *Strategy) HasSignificantEdge(gc *game.GameContext) bool {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return false
	}
	for _, e := range []float64{
		hs.EdgeHomeYes, hs.EdgeAwayYes,
		hs.EdgeHomeNo, hs.EdgeAwayNo,
	} {
		if e >= discrepancyPct {
			return true
		}
	}
	return false
}

func (s *Strategy) OnPriceUpdate(gc *game.GameContext) []events.OrderIntent {
	return nil
}

// updatePowerPlay compares the parsed power play / penalty data against
// HockeyState and fires gc.OnPowerPlayChange when it transitions.
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
		hs.IsHomePowerPlay = homeOn
		hs.IsAwayPowerPlay = awayOn
		if gc.OnPowerPlayChange != nil {
			gc.OnPowerPlayChange(gc, homeOn, awayOn)
		}
	}
}

type edge struct {
	ticker   string
	outcome  string
	edgeVal  float64
	modelPct float64
}

func (s *Strategy) findEdges(hs *hockeyState.HockeyState) []edge {
	var edges []edge
	for _, e := range []edge{
		{hs.HomeTicker, "home", hs.EdgeHomeYes, hs.ModelHomePct},
		{hs.AwayTicker, "away", hs.EdgeAwayYes, hs.ModelAwayPct},
	} {
		if e.ticker == "" || e.edgeVal < discrepancyPct {
			continue
		}
		edges = append(edges, e)
	}
	return edges
}

// buildOrderIntent creates order intents for detected edges.
// Limit prices are set 3¢ below model probability as a buffer.
func (s *Strategy) buildOrderIntent(gc *game.GameContext, hs *hockeyState.HockeyState, edges []edge, overturn bool) []events.OrderIntent {
	var intents []events.OrderIntent
	for _, e := range edges {
		td := gc.Tickers[e.ticker]

		reason := fmt.Sprintf("model %.1f%% vs kalshi %.0f¢ (+%.1f%%)", e.modelPct, td.YesAsk, e.edgeVal)

		intents = append(intents, events.OrderIntent{
			Sport:     gc.Sport,
			League:    gc.League,
			GameID:    gc.EID,
			EID:       gc.EID,
			Ticker:    e.ticker,
			Side:      "yes",
			Outcome:   e.outcome,
			LimitPct:  e.modelPct - 3,
			Reason:    reason,
			HomeScore: hs.HomeScore,
			AwayScore: hs.AwayScore,
			Overturn:  overturn,
		})

		oppTicker, oppOutcome := s.oppositeSide(hs, e.outcome)
		if oppTicker != "" {
			intents = append(intents, events.OrderIntent{
				Sport:     gc.Sport,
				League:    gc.League,
				GameID:    gc.EID,
				EID:       gc.EID,
				Ticker:    oppTicker,
				Side:      "no",
				Outcome:   oppOutcome,
				LimitPct:  100 - e.modelPct - 3,
				Reason:    reason,
				HomeScore: hs.HomeScore,
				AwayScore: hs.AwayScore,
				Overturn:  overturn,
			})
		}
	}
	return intents
}

func (s *Strategy) oppositeSide(hs *hockeyState.HockeyState, outcome string) (ticker, oppOutcome string) {
	if outcome == "home" && hs.AwayTicker != "" {
		return hs.AwayTicker, "away"
	}
	if outcome == "away" && hs.HomeTicker != "" {
		return hs.HomeTicker, "home"
	}
	return "", ""
}

// ── Pregame odds infrastructure ──────────────────────────────────────

func (s *Strategy) applyPregame(hs *hockeyState.HockeyState, homeTeam, awayTeam string) bool {
	if s.pregame == nil {
		return false
	}

	s.pregameMu.RLock()
	stale := time.Since(s.pregameFetch) > pregameCacheTTL
	cached := s.pregameCache
	s.pregameMu.RUnlock()

	if stale || cached == nil {
		go s.refreshPregameCache()
		if cached == nil {
			return false
		}
	}

	homeNorm := ticker.Normalize(homeTeam, ticker.HockeyAliases)
	awayNorm := ticker.Normalize(awayTeam, ticker.HockeyAliases)

	for _, p := range cached {
		pHome := ticker.Normalize(p.HomeTeam, ticker.HockeyAliases)
		pAway := ticker.Normalize(p.AwayTeam, ticker.HockeyAliases)

		if (fuzzyTeamMatch(pHome, homeNorm) && fuzzyTeamMatch(pAway, awayNorm)) ||
			(fuzzyTeamMatch(pHome, awayNorm) && fuzzyTeamMatch(pAway, homeNorm)) {
			hs.HomeStrength = p.HomePregameStrength
			hs.AwayStrength = p.AwayPregameStrength
			if p.G0 > 0 {
				g0 := p.G0
				hs.PregameG0 = &g0
			}
			telemetry.Debugf("pregame: matched %s vs %s -> H=%.1f%% A=%.1f%%",
				homeTeam, awayTeam, p.HomePregameStrength*100, p.AwayPregameStrength*100)
			return true
		}
	}
	telemetry.Debugf("pregame: no match for %s vs %s", homeTeam, awayTeam)
	return false
}

func (s *Strategy) refreshPregameCache() {
	s.pregameMu.Lock()
	if s.pregameCache != nil && time.Since(s.pregameFetch) < pregameCacheTTL {
		s.pregameMu.Unlock()
		return
	}
	if time.Since(s.pregameLastTry) < pregameRetryCool {
		s.pregameMu.Unlock()
		return
	}
	s.pregameLastTry = time.Now()
	s.pregameMu.Unlock()

	fetched, err := s.pregame.FetchHockeyPregame()
	if err != nil {
		telemetry.Warnf("pregame: fetch failed: %v", err)
		return
	}

	if fetched == nil {
		fetched = []odds.PregameOdds{}
	}

	s.pregameMu.Lock()
	s.pregameCache = fetched
	s.pregameFetch = time.Now()
	s.pregameMu.Unlock()
}

func fuzzyTeamMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b || strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	// Webhook sends abbreviated names (e.g. "ont reign") while GoalServe
	// uses full names ("ontario reign"). Fall back to checking whether any
	// significant word (>=4 chars) appears in both strings.
	for _, w := range strings.Fields(a) {
		if len(w) >= 4 && strings.Contains(b, w) {
			return true
		}
	}
	for _, w := range strings.Fields(b) {
		if len(w) >= 4 && strings.Contains(a, w) {
			return true
		}
	}
	return false
}

// slamOrders emits high-confidence paired orders when a game finishes with a clear winner:
// YES on the winner's ticker + NO on the loser's ticker.
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
		LimitPct:  1,
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
			LimitPct:  1,
			Reason:    reason,
			HomeScore: gu.HomeScore,
			AwayScore: gu.AwayScore,
			Slam:      true,
		})
	}

	return intents
}
