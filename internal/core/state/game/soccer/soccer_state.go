package soccer

import (
	"strings"
	"time"
)

type RedCard struct {
	Team                  int     // 1=home, 2=away
	MinutesRemainingAtRed float64
}

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

	RedCards []RedCard

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

	ExtraTimeSettlesML bool

	hasLiveData           bool
	scoreDropPending      bool
	scoreDropData         *scoreDropRecord
	regHomeFrozen         *int
	regAwayFrozen         *int
	regulationScoreFrozen bool
	orderedTrades         map[tradeScoreKey]bool
	finaled               bool
}

type scoreDropRecord struct {
	firstSeen time.Time
	homeScore int
	awayScore int
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
		HomeWinPct:    0.40,
		DrawPct:       0.25,
		AwayWinPct:    0.35,
		G0:            2.5,
		TimeLeft:      90,
		orderedTrades: make(map[tradeScoreKey]bool),
	}
}

func (s *SoccerState) GetEID() string           { return s.EID }
func (s *SoccerState) GetHomeTeam() string       { return s.HomeTeam }
func (s *SoccerState) GetAwayTeam() string       { return s.AwayTeam }
func (s *SoccerState) GetHomeScore() int         { return s.HomeScore }
func (s *SoccerState) GetAwayScore() int         { return s.AwayScore }
func (s *SoccerState) GetPeriod() string         { return s.Half }
func (s *SoccerState) GetTimeRemaining() float64 { return s.TimeLeft }
func (s *SoccerState) HasLiveData() bool         { return s.hasLiveData }

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

func (s *SoccerState) IsScoreDropPending() bool { return s.scoreDropPending }

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
	prevTotal := s.HomeScore + s.AwayScore
	newTotal := homeScore + awayScore

	if newTotal >= prevTotal {
		if s.scoreDropPending {
			s.ClearScoreDropPending()
		}
		return "accept"
	}

	now := time.Now()
	if s.scoreDropData != nil {
		if homeScore == s.scoreDropData.homeScore && awayScore == s.scoreDropData.awayScore {
			if now.Sub(s.scoreDropData.firstSeen) >= time.Duration(confirmSec)*time.Second {
				s.ClearScoreDropPending()
				return "confirmed"
			}
		} else {
			s.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: homeScore, awayScore: awayScore}
		}
		s.scoreDropPending = true
		return "pending"
	}

	s.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: homeScore, awayScore: awayScore}
	s.scoreDropPending = true
	return "new_drop"
}

func (s *SoccerState) ClearScoreDropPending() {
	s.scoreDropPending = false
	s.scoreDropData = nil
}

func (s *SoccerState) AddRedCard(team int, minutesRemaining float64) {
	s.RedCards = append(s.RedCards, RedCard{
		Team:                  team,
		MinutesRemainingAtRed: minutesRemaining,
	})
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

func (s *SoccerState) HasOrdered(outcome string) bool {
	return s.orderedTrades[tradeScoreKey{key: outcome, homeScore: s.HomeScore, awayScore: s.AwayScore}]
}

func (s *SoccerState) MarkOrdered(outcome string) {
	s.orderedTrades[tradeScoreKey{key: outcome, homeScore: s.HomeScore, awayScore: s.AwayScore}] = true
}

func (s *SoccerState) ClearOrdered() {
	s.orderedTrades = make(map[tradeScoreKey]bool)
}

func (s *SoccerState) RedCardCounts() (home, away int) {
	for _, rc := range s.RedCards {
		if rc.Team == 1 {
			home++
		} else {
			away++
		}
	}
	return
}

func (s *SoccerState) HalfNumber() int {
	h := strings.ToLower(strings.TrimSpace(s.Half))
	if strings.Contains(h, "2nd") || strings.Contains(h, "second") {
		return 2
	}
	return 1
}

func (s *SoccerState) SetPregameOdds(homeP, drawP, awayP, g0 float64) {
	s.HomeWinPct = homeP
	s.DrawPct = drawP
	s.AwayWinPct = awayP
	s.G0 = g0
}
