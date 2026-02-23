package soccer

import (
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/core/odds"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/core/ticker"
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
		fetched, err := s.pregame.FetchSoccerPregame()
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
		telemetry.Infof("pregame: loaded %d soccer matches on startup", len(fetched))
		return
	}
	telemetry.Errorf("pregame: all %d startup attempts failed — proceeding without pregame data", startupMaxAttempts)
}

func (s *Strategy) Evaluate(gc *game.GameContext, gu *events.GameUpdateEvent) strategy.EvalResult {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return strategy.EvalResult{}
	}

	if s.pregame != nil {
		if _, applied := s.pregameApplied.Load(gc.EID); !applied {
			if s.applyPregame(ss, gu.HomeTeam, gu.AwayTeam) {
				ss.PregameApplied = true
				s.pregameApplied.Store(gc.EID, true)
			}
		}
	}

	prevHomeRC, prevAwayRC := ss.HomeRedCards, ss.AwayRedCards

	// Always update red cards before the score-change guard so they
	// are never silently dropped on a no-score-change webhook.
	if gu.HomeRedCards > 0 || gu.AwayRedCards > 0 {
		ss.UpdateRedCards(gu.HomeRedCards, gu.AwayRedCards)
	}
	rcChanged := ss.HomeRedCards != prevHomeRC || ss.AwayRedCards != prevAwayRC

	tracked := len(gc.Tickers) > 0

	if ss.HasLiveData() {
		result := ss.CheckScoreDrop(gu.HomeScore, gu.AwayScore, s.scoreDropConfirmSec)
		switch result {
		case "new_drop":
			if tracked {
				telemetry.Infof("soccer: score drop %s for %s @ %s [%s] (%d-%d -> %d-%d)",
					result, ss.AwayTeam, ss.HomeTeam, gu.EID,
					ss.GetHomeScore(), ss.GetAwayScore(), gu.HomeScore, gu.AwayScore)
			}
			s.lastPendingLog = time.Now()
			return strategy.EvalResult{
				RedCardChanged: rcChanged,
				RedCardsHome:   ss.HomeRedCards,
				RedCardsAway:   ss.AwayRedCards,
			}
		case "pending":
			if tracked && time.Since(s.lastPendingLog) >= 20*time.Second {
				telemetry.Infof("soccer: score drop %s for %s @ %s [%s] (%d-%d -> %d-%d)",
					result, ss.AwayTeam, ss.HomeTeam, gu.EID,
					ss.GetHomeScore(), ss.GetAwayScore(), gu.HomeScore, gu.AwayScore)
				s.lastPendingLog = time.Now()
			}
			return strategy.EvalResult{
				RedCardChanged: rcChanged,
				RedCardsHome:   ss.HomeRedCards,
				RedCardsAway:   ss.AwayRedCards,
			}
		case "confirmed":
			if tracked {
				telemetry.Infof("soccer: overturn confirmed for %s @ %s [%s] -> %d-%d",
					ss.AwayTeam, ss.HomeTeam, gu.EID, gu.HomeScore, gu.AwayScore)
			}
		}
	}

	changed := ss.UpdateScore(gu.HomeScore, gu.AwayScore, gu.Period, gu.TimeLeft)
	if !changed {
		return strategy.EvalResult{
			RedCardChanged: rcChanged,
			RedCardsHome:   ss.HomeRedCards,
			RedCardsAway:   ss.AwayRedCards,
		}
	}

	telemetry.Metrics.ScoreChanges.Inc()

	ss.PinnacleUpdated = false
	if gu.HomeWinPct != nil && gu.DrawPct != nil && gu.AwayWinPct != nil {
		h := *gu.HomeWinPct * 100
		d := *gu.DrawPct * 100
		a := *gu.AwayWinPct * 100
		ss.PinnacleUpdated = ss.PinnacleHomePct == nil || *ss.PinnacleHomePct != h
		ss.PinnacleHomePct = &h
		ss.PinnacleDrawPct = &d
		ss.PinnacleAwayPct = &a
	}

	return strategy.EvalResult{
		RedCardChanged: rcChanged,
		RedCardsHome:   ss.HomeRedCards,
		RedCardsAway:   ss.AwayRedCards,
	}
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

	homeNorm := ticker.Normalize(homeTeam, ticker.SoccerAliases)
	awayNorm := ticker.Normalize(awayTeam, ticker.SoccerAliases)

	for _, p := range cached {
		pHome := ticker.Normalize(p.HomeTeam, ticker.SoccerAliases)
		pAway := ticker.Normalize(p.AwayTeam, ticker.SoccerAliases)

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
