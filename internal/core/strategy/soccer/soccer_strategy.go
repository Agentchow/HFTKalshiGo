package soccer

import (
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/core/odds"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	pregameCacheTTL  = 30 * time.Minute
	pregameRetryCool = 30 * time.Second
	edgeThreshold    = 3.0 // percentage points
)

// PregameOddsProvider abstracts fetching pregame odds.
// Satisfied by *goalserve_http.PregameClient.
type PregameOddsProvider interface {
	FetchSoccerPregame() ([]odds.PregameOdds, error)
}

// Strategy implements soccer-specific 3-way (1X2) trading logic.
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
		s.refreshPregameCache()
	}
	return s
}

func (s *Strategy) Evaluate(gc *game.GameContext, sc *events.ScoreChangeEvent) strategy.EvalResult {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return strategy.EvalResult{}
	}

	var displayEvents []string

	// Apply pregame odds on first encounter.
	if s.pregame != nil {
		if _, applied := s.pregameApplied.Load(gc.EID); !applied {
			if s.applyPregame(ss, sc.HomeTeam, sc.AwayTeam) {
				s.pregameApplied.Store(gc.EID, true)
				displayEvents = append(displayEvents, "LIVE")
			}
		}
	}

	// Snapshot red cards before state update.
	prevHomeRC, prevAwayRC := ss.HomeRedCards, ss.AwayRedCards

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
			return strategy.EvalResult{DisplayEvents: displayEvents}
		case "pending":
			if tracked && time.Since(s.lastPendingLog) >= 20*time.Second {
				telemetry.Infof("soccer: score drop %s for %s @ %s [%s] (%d-%d -> %d-%d)",
					result, ss.AwayTeam, ss.HomeTeam, sc.EID,
					ss.GetHomeScore(), ss.GetAwayScore(), sc.HomeScore, sc.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{DisplayEvents: displayEvents}
		case "confirmed":
			if tracked {
				telemetry.Infof("soccer: overturn confirmed for %s @ %s [%s] -> %d-%d",
					ss.AwayTeam, ss.HomeTeam, sc.EID, sc.HomeScore, sc.AwayScore)
			}
		}
	}

	changed := ss.UpdateScore(sc.HomeScore, sc.AwayScore, sc.Period, sc.TimeLeft)
	if !changed {
		return strategy.EvalResult{DisplayEvents: displayEvents}
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

	if ss.HomeRedCards != prevHomeRC || ss.AwayRedCards != prevAwayRC {
		displayEvents = append(displayEvents, "RED-CARD")
	}

	// TODO: plug in Poisson model and 6-way edge evaluation
	return strategy.EvalResult{DisplayEvents: displayEvents}
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

func (s *Strategy) HasSignificantEdge(gc *game.GameContext) bool {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return false
	}
	for _, e := range []float64{
		ss.EdgeHomeYes, ss.EdgeDrawYes, ss.EdgeAwayYes,
		ss.EdgeHomeNo, ss.EdgeDrawNo, ss.EdgeAwayNo,
	} {
		if e >= edgeThreshold {
			return true
		}
	}
	return false
}

// ── Pregame odds infrastructure ──────────────────────────────────────

func (s *Strategy) applyPregame(ss *soccerState.SoccerState, homeTeam, awayTeam string) bool {
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

	homeNorm := strings.ToLower(strings.TrimSpace(homeTeam))
	awayNorm := strings.ToLower(strings.TrimSpace(awayTeam))

	for _, p := range cached {
		pHome := strings.ToLower(strings.TrimSpace(p.HomeTeam))
		pAway := strings.ToLower(strings.TrimSpace(p.AwayTeam))

		if (fuzzyTeamMatch(pHome, homeNorm) && fuzzyTeamMatch(pAway, awayNorm)) ||
			(fuzzyTeamMatch(pHome, awayNorm) && fuzzyTeamMatch(pAway, homeNorm)) {
			ss.HomeWinPct = p.HomeWinPct
			ss.DrawPct = p.DrawPct
			ss.AwayWinPct = p.AwayWinPct
			ss.G0 = p.G0
			telemetry.Debugf("pregame: matched %s vs %s -> H=%.1f%% D=%.1f%% A=%.1f%% G0=%.2f",
				homeTeam, awayTeam, p.HomeWinPct*100, p.DrawPct*100, p.AwayWinPct*100, p.G0)
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

	fetched, err := s.pregame.FetchSoccerPregame()
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
	return a == b || strings.Contains(a, b) || strings.Contains(b, a)
}
