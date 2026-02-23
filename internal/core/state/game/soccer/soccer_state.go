package soccer

import (
	"strings"

	game "github.com/charleschow/hft-trading/internal/core/state/game"
)

// SoccerState holds live state for a single soccer match.
// Mirrors SoccerGame from the Python codebase.
type SoccerState struct {
	EID       string
	League    string
	HomeTeam  string
	AwayTeam  string
	HomeScore int
	AwayScore int
	Half      string  // "1st Half", "2nd Half", "Half Time", "Extra Time", etc.
	TimeLeft  float64 // minutes remaining in regulation

	HomeWinPct float64 // pregame 1X2 probs (0–1)
	DrawPct    float64
	AwayWinPct float64
	G0         float64 // expected total goals

	HomeRedCards int
	AwayRedCards int

	HomeTicker string
	DrawTicker string
	AwayTicker string
	SeriesSlug string
	SeriesName string

	ModelHomeYes float64 // 0–100
	ModelDrawYes float64
	ModelAwayYes float64
	ModelHomeNo  float64
	ModelDrawNo  float64
	ModelAwayNo  float64

	PinnacleHomePct *float64
	PinnacleDrawPct *float64
	PinnacleAwayPct *float64

	EdgeHomeYes float64
	EdgeDrawYes float64
	EdgeAwayYes float64
	EdgeHomeNo  float64
	EdgeDrawNo  float64
	EdgeAwayNo  float64

	ExtraTimeSettlesML bool
	PregameApplied     bool

	game.ScoreDropTracker

	hasLiveData           bool
	regHomeFrozen         *int
	regAwayFrozen         *int
	regulationScoreFrozen bool
	orderedTrades         map[tradeScoreKey]bool
	finaled               bool
}

type tradeScoreKey struct {
	key       string
	homeScore int
	awayScore int
}

func New(eid, league, homeTeam, awayTeam string) *SoccerState {
	return &SoccerState{
		EID:           eid,
		League:        league,
		HomeTeam:      homeTeam,
		AwayTeam:      awayTeam,
		HomeWinPct:    0.0,
		DrawPct:       0.0,
		AwayWinPct:    0.0,
		G0:            0,
		TimeLeft:      999,
		ModelHomeYes:  100,
		ModelDrawYes:  100,
		ModelAwayYes:  100,
		orderedTrades: make(map[tradeScoreKey]bool),
	}
}

func (s *SoccerState) GetEID() string            { return s.EID }
func (s *SoccerState) GetHomeTeam() string       { return s.HomeTeam }
func (s *SoccerState) GetAwayTeam() string       { return s.AwayTeam }
func (s *SoccerState) GetHomeScore() int         { return s.HomeScore }
func (s *SoccerState) GetAwayScore() int         { return s.AwayScore }
func (s *SoccerState) GetPeriod() string         { return s.Half }
func (s *SoccerState) GetTimeRemaining() float64 { return s.TimeLeft }
func (s *SoccerState) HasLiveData() bool         { return s.hasLiveData }
func (s *SoccerState) HasPregame() bool          { return s.PregameApplied }

func (s *SoccerState) GoalDiff() int { return s.HomeScore - s.AwayScore }

func (s *SoccerState) IsHalfTime() bool {
	h := strings.ToLower(strings.TrimSpace(s.Half))
	return h == "half time" || h == "halftime" || h == "ht"
}

func (s *SoccerState) IsExtraTime() bool {
	h := strings.ToLower(strings.TrimSpace(s.Half))
	return (strings.Contains(h, "extra") || h == "et") && !s.IsFinished()
}

func (s *SoccerState) IsPenalties() bool {
	h := strings.ToLower(strings.TrimSpace(s.Half))
	return h == "penalties" || h == "penalty" || h == "pen" || h == "penalty shootout"
}

func (s *SoccerState) IsFinished() bool {
	h := strings.ToLower(strings.TrimSpace(s.Half))
	finished := []string{
		"finished", "ended", "final", "ft",
		"after extra time", "aet",
		"after penalties", "after pen",
	}
	for _, f := range finished {
		if h == f {
			return true
		}
	}
	return false
}

func (s *SoccerState) IsRegulationOver() bool {
	return s.IsFinished() || s.IsExtraTime() || s.IsPenalties()
}

func (s *SoccerState) IsLive() bool {
	return s.Half != "" && !s.IsFinished()
}

func (s *SoccerState) UpdateScore(homeScore, awayScore int, half string, timeRemain float64) bool {
	firstUpdate := !s.hasLiveData
	scoreChanged := s.HomeScore != homeScore || s.AwayScore != awayScore

	if s.hasLiveData && half == s.Half && timeRemain > s.TimeLeft {
		timeRemain = s.TimeLeft
	}

	// Freeze regulation score on transition to ET / penalties
	if !s.regulationScoreFrozen && isHalfPostRegulation(half) {
		if firstUpdate {
			s.regHomeFrozen = intPtr(homeScore)
			s.regAwayFrozen = intPtr(awayScore)
		} else {
			s.regHomeFrozen = intPtr(s.HomeScore)
			s.regAwayFrozen = intPtr(s.AwayScore)
		}
		s.regulationScoreFrozen = true
	}

	s.HomeScore = homeScore
	s.AwayScore = awayScore
	s.Half = half
	s.TimeLeft = timeRemain
	s.hasLiveData = true

	return firstUpdate || scoreChanged
}

func (s *SoccerState) CheckScoreDrop(homeScore, awayScore int, confirmSec int) string {
	return s.ScoreDropTracker.CheckDrop(s.HomeScore, s.AwayScore, homeScore, awayScore, confirmSec)
}

// UpdateRedCards sets the current counts.
func (s *SoccerState) UpdateRedCards(home, away int) {
	s.HomeRedCards = home
	s.AwayRedCards = away
}

func (s *SoccerState) RegulationGoalDiff() int {
	if s.regulationScoreFrozen && s.regHomeFrozen != nil && s.regAwayFrozen != nil {
		return *s.regHomeFrozen - *s.regAwayFrozen
	}
	return s.GoalDiff()
}

func isHalfPostRegulation(half string) bool {
	h := strings.ToLower(strings.TrimSpace(half))
	return strings.Contains(h, "extra") || h == "et" ||
		h == "penalties" || h == "penalty" || h == "pen" || h == "penalty shootout" ||
		h == "finished" || h == "ended" || h == "final" || h == "ft" ||
		h == "after extra time" || h == "aet" ||
		h == "after penalties" || h == "after pen"
}

func intPtr(v int) *int { return &v }

func (s *SoccerState) SetTickers(home, away, draw string) {
	s.HomeTicker = home
	s.AwayTicker = away
	s.DrawTicker = draw
}

func (s *SoccerState) RecalcEdge(tickers map[string]*game.TickerData) {
	if s.PinnacleHomePct == nil || s.PinnacleDrawPct == nil || s.PinnacleAwayPct == nil {
		return
	}
	pinnHome := *s.PinnacleHomePct
	pinnDraw := *s.PinnacleDrawPct
	pinnAway := *s.PinnacleAwayPct

	s.EdgeHomeYes = edgeFor(pinnHome, yesAsk(tickers, s.HomeTicker))
	s.EdgeDrawYes = edgeFor(pinnDraw, yesAsk(tickers, s.DrawTicker))
	s.EdgeAwayYes = edgeFor(pinnAway, yesAsk(tickers, s.AwayTicker))
	s.EdgeHomeNo = edgeFor(120-pinnHome, noAsk(tickers, s.HomeTicker))
	s.EdgeDrawNo = edgeFor(100-pinnDraw, noAsk(tickers, s.DrawTicker))
	s.EdgeAwayNo = edgeFor(100-pinnAway, noAsk(tickers, s.AwayTicker))
}

func edgeFor(model, ask float64) float64 {
	if ask <= 0 {
		return 0
	}
	return model - ask
}

func yesAsk(tickers map[string]*game.TickerData, ticker string) float64 {
	if td, ok := tickers[ticker]; ok {
		return td.YesAsk
	}
	return -1
}

func noAsk(tickers map[string]*game.TickerData, ticker string) float64 {
	if td, ok := tickers[ticker]; ok {
		return td.NoAsk
	}
	return -1
}
