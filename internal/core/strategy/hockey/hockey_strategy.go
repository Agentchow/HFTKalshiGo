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
// On each score change it:
//  1. Updates the game state
//  2. Runs the score-drop guard
//  3. Recomputes model probabilities (Pinnacle first, math model fallback)
//  4. Compares model vs market and emits OrderIntents for edges
type Strategy struct {
	scoreDropConfirmSec int
	lastPendingLog      time.Time

	pregame        PregameOddsProvider
	pregameMu      sync.RWMutex
	pregameCache   []odds.PregameOdds
	pregameFetch   time.Time
	pregameLastTry time.Time
	pregameApplied sync.Map
}

func NewStrategy(scoreDropConfirmSec int, pregame PregameOddsProvider) *Strategy {
	s := &Strategy{
		scoreDropConfirmSec: scoreDropConfirmSec,
		pregame:             pregame,
	}
	if pregame != nil {
		s.loadPregameWithRetry()
	}
	return s
}

const (
	startupMaxAttempts = 5
	startupRetryDelay  = 10 * time.Second
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

	// Score-drop guard
	if hs.HasLiveData() {
		result := hs.CheckScoreDrop(gu.HomeScore, gu.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "new_drop":
			telemetry.Infof("hockey: score drop %s for %s (%d-%d -> %d-%d)",
				result, gu.EID, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{}
		case "pending":
			if time.Since(s.lastPendingLog) >= 20*time.Second {
				telemetry.Infof("hockey: score drop %s for %s (%d-%d -> %d-%d)",
					result, gu.EID, hs.GetHomeScore(), hs.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{}
		case "confirmed":
			hs.ClearOrdered()
			telemetry.Infof("hockey: overturn confirmed for %s -> %d-%d",
				gu.EID, gu.HomeScore, gu.AwayScore)
		}
	}

	changed := hs.UpdateScore(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	if !changed {
		return strategy.EvalResult{}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	hs.PinnacleUpdated = false
	if gu.HomeWinPct != nil && gu.AwayWinPct != nil {
		h := *gu.HomeWinPct * 100
		a := *gu.AwayWinPct * 100
		hs.PinnacleUpdated = hs.PinnacleHomePct == nil || *hs.PinnacleHomePct != h
		hs.PinnacleHomePct = &h
		hs.PinnacleAwayPct = &a
	}

	// Compute model: Pinnacle first, math model fallback
	s.computeModel(hs)

	return strategy.EvalResult{
		Intents: s.checkEdges(gc, hs),
	}
}

func (s *Strategy) OnFinish(gc *game.GameContext, gu *events.GameUpdateEvent) []events.OrderIntent {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return nil
	}

	return s.slamOrders(gc, hs, gu)
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
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok || !hs.HasLiveData() || hs.ModelHomePct == 0 {
		return nil
	}
	return s.checkEdges(gc, hs)
}

// checkEdges reads stored edge values from HockeyState and returns
// OrderIntents for any edges above the discrepancy threshold.
func (s *Strategy) checkEdges(gc *game.GameContext, hs *hockeyState.HockeyState) []events.OrderIntent {
	var intents []events.OrderIntent

	for _, edge := range []struct {
		ticker   string
		outcome  string
		edgeVal  float64
		modelPct float64
	}{
		{hs.HomeTicker, "home", hs.EdgeHomeYes, hs.ModelHomePct},
		{hs.AwayTicker, "away", hs.EdgeAwayYes, hs.ModelAwayPct},
	} {
		if edge.ticker == "" || edge.edgeVal < discrepancyPct {
			continue
		}
		if hs.HasOrdered(edge.outcome) {
			continue
		}

		td := gc.Tickers[edge.ticker]
		hs.MarkOrdered(edge.outcome)
		intents = append(intents, events.OrderIntent{
			Sport:     gc.Sport,
			League:    gc.League,
			GameID:    gc.EID,
			EID:       gc.EID,
			Ticker:    edge.ticker,
			Side:      "yes",
			Outcome:   edge.outcome,
			LimitPct:  edge.modelPct,
			Reason:    fmt.Sprintf("model %.1f%% vs kalshi %.0f¢ (+%.1f%%)", edge.modelPct, td.YesAsk, edge.edgeVal),
			HomeScore: hs.HomeScore,
			AwayScore: hs.AwayScore,
		})
	}

	return intents
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
			hs.HomeWinPct = p.HomeWinPct
			hs.AwayWinPct = p.AwayWinPct
			if p.G0 > 0 {
				g0 := p.G0
				hs.PregameG0 = &g0
			}
			telemetry.Debugf("pregame: matched %s vs %s -> H=%.1f%% A=%.1f%%",
				homeTeam, awayTeam, p.HomeWinPct*100, p.AwayWinPct*100)
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

// slamOrders emits high-confidence orders when a game finishes with a clear winner.
func (s *Strategy) slamOrders(gc *game.GameContext, hs *hockeyState.HockeyState, gu *events.GameUpdateEvent) []events.OrderIntent {
	if gu.HomeScore == gu.AwayScore {
		return nil
	}

	var winTicker, outcome string
	if gu.HomeScore > gu.AwayScore {
		winTicker = hs.HomeTicker
		outcome = "home"
	} else {
		winTicker = hs.AwayTicker
		outcome = "away"
	}

	if winTicker == "" {
		return nil
	}

	return []events.OrderIntent{{
		Sport:     gu.Sport,
		League:    gu.League,
		GameID:    gu.EID,
		EID:       gu.EID,
		Ticker:    winTicker,
		Side:      "yes",
		Outcome:   outcome,
		LimitPct:  99,
		Reason:    fmt.Sprintf("game finished %d-%d", gu.HomeScore, gu.AwayScore),
		HomeScore: gu.HomeScore,
		AwayScore: gu.AwayScore,
	}}
}
