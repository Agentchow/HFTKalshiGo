package football

import (
	"strings"
	"time"
)

// FootballState holds live state for a single American football game.
type FootballState struct {
	EID       string
	League    string // "NFL", "NCAAF"
	HomeTeam  string
	AwayTeam  string
	HomeScore int
	AwayScore int
	Quarter   string  // "Q1", "Q2", "Halftime", "Q3", "Q4", "OT"
	TimeLeft  float64 // minutes remaining

	HomeWinPct float64
	AwayWinPct float64

	HomeTicker string
	AwayTicker string
	SeriesSlug string
	SeriesName string

	ModelHomePct float64 // 0–100
	ModelAwayPct float64

	hasLiveData      bool
	scoreDropPending bool
	scoreDropData    *scoreDropRecord
	orderedSides     map[scoreKey]bool
	finaled          bool
}

type scoreDropRecord struct {
	firstSeen time.Time
	homeScore int
	awayScore int
}

type scoreKey struct {
	side      string
	homeScore int
	awayScore int
}

func New(eid, league, homeTeam, awayTeam string) *FootballState {
	return &FootballState{
		EID:          eid,
		League:       league,
		HomeTeam:     homeTeam,
		AwayTeam:     awayTeam,
		HomeWinPct:   0.5,
		AwayWinPct:   0.5,
		TimeLeft:     60,
		orderedSides: make(map[scoreKey]bool),
	}
}

func (f *FootballState) GetEID() string           { return f.EID }
func (f *FootballState) GetHomeTeam() string       { return f.HomeTeam }
func (f *FootballState) GetAwayTeam() string       { return f.AwayTeam }
func (f *FootballState) GetHomeScore() int         { return f.HomeScore }
func (f *FootballState) GetAwayScore() int         { return f.AwayScore }
func (f *FootballState) GetPeriod() string         { return f.Quarter }
func (f *FootballState) GetTimeRemaining() float64 { return f.TimeLeft }
func (f *FootballState) HasLiveData() bool         { return f.hasLiveData }

func (f *FootballState) Lead() int { return f.HomeScore - f.AwayScore }

func (f *FootballState) IsOvertime() bool {
	q := strings.ToLower(strings.TrimSpace(f.Quarter))
	return strings.Contains(q, "overtime") || q == "ot"
}

func (f *FootballState) IsFinished() bool {
	q := strings.ToLower(strings.TrimSpace(f.Quarter))
	return q == "finished" || q == "final" || q == "ended" ||
		strings.Contains(q, "after overtime")
}

func (f *FootballState) IsLive() bool {
	return f.Quarter != "" && !f.IsFinished()
}

func (f *FootballState) UpdateScore(homeScore, awayScore int, quarter string, timeRemain float64) bool {
	firstUpdate := !f.hasLiveData
	scoreChanged := f.HomeScore != homeScore || f.AwayScore != awayScore

	if f.hasLiveData && quarter == f.Quarter && timeRemain > f.TimeLeft {
		timeRemain = f.TimeLeft
	}

	f.HomeScore = homeScore
	f.AwayScore = awayScore
	f.Quarter = quarter
	f.TimeLeft = timeRemain
	f.hasLiveData = true

	return firstUpdate || scoreChanged
}

func (f *FootballState) CheckScoreDrop(homeScore, awayScore int, confirmSec int) string {
	prevTotal := f.HomeScore + f.AwayScore
	newTotal := homeScore + awayScore

	if newTotal >= prevTotal {
		if f.scoreDropPending {
			f.ClearScoreDropPending()
		}
		return "accept"
	}

	now := time.Now()
	if f.scoreDropData != nil {
		if homeScore == f.scoreDropData.homeScore && awayScore == f.scoreDropData.awayScore {
			if now.Sub(f.scoreDropData.firstSeen) >= time.Duration(confirmSec)*time.Second {
				f.ClearScoreDropPending()
				return "confirmed"
			}
		} else {
			f.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: homeScore, awayScore: awayScore}
		}
		f.scoreDropPending = true
		return "pending"
	}

	f.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: homeScore, awayScore: awayScore}
	f.scoreDropPending = true
	return "new_drop"
}

func (f *FootballState) ClearScoreDropPending() {
	f.scoreDropPending = false
	f.scoreDropData = nil
}

func (f *FootballState) IsScoreDropPending() bool { return f.scoreDropPending }

func (f *FootballState) SetTickers(home, away, _ string) {
	f.HomeTicker = home
	f.AwayTicker = away
}
