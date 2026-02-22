package hockey

import (
	"strings"
	"time"
)

// HockeyState holds the live state for a single hockey game.
// Mirrors HockeyGame from the Python codebase.
type HockeyState struct {
	EID       string
	League    string // "AHL", "NHL", "ECHL"
	HomeTeam  string
	AwayTeam  string
	HomeScore int
	AwayScore int
	Period    string
	TimeLeft  float64 // minutes remaining

	HomeWinPct float64 // pregame strength (0–1)
	AwayWinPct float64

	HomeTicker string
	AwayTicker string
	SeriesSlug string
	SeriesName string

	ModelHomePct float64 // 0–100
	ModelAwayPct float64 // 0–100

	// Pinnacle odds cache
	PinnacleHomePct *float64 // 0–100
	PinnacleAwayPct *float64 // 0–100

	hasLiveData      bool
	scoreDropPending bool
	scoreDropData    *scoreDropRecord
	orderedSides     map[scoreKey]bool
	finaled          bool
	shootoutLogged   bool
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

func New(eid, league, homeTeam, awayTeam string) *HockeyState {
	return &HockeyState{
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

func (h *HockeyState) GetEID() string           { return h.EID }
func (h *HockeyState) GetHomeTeam() string       { return h.HomeTeam }
func (h *HockeyState) GetAwayTeam() string       { return h.AwayTeam }
func (h *HockeyState) GetHomeScore() int         { return h.HomeScore }
func (h *HockeyState) GetAwayScore() int         { return h.AwayScore }
func (h *HockeyState) GetPeriod() string         { return h.Period }
func (h *HockeyState) GetTimeRemaining() float64 { return h.TimeLeft }
func (h *HockeyState) HasLiveData() bool         { return h.hasLiveData }

func (h *HockeyState) Lead() int { return h.HomeScore - h.AwayScore }

func (h *HockeyState) IsOvertime() bool {
	p := strings.ToLower(strings.TrimSpace(h.Period))
	return strings.Contains(p, "overtime") || p == "ot" || p == "penalties"
}

func (h *HockeyState) IsFinished() bool {
	p := strings.ToLower(strings.TrimSpace(h.Period))
	return p == "finished" || p == "final" || p == "ended" ||
		strings.Contains(p, "after over time") ||
		strings.Contains(p, "after overtime") ||
		strings.Contains(p, "after ot")
}

func (h *HockeyState) IsLive() bool {
	return h.Period != "" && !h.IsFinished()
}

func (h *HockeyState) UpdateScore(homeScore, awayScore int, period string, timeRemain float64) bool {
	firstUpdate := !h.hasLiveData
	scoreChanged := h.HomeScore != homeScore || h.AwayScore != awayScore

	// Monotonic decrease guard
	if h.hasLiveData && period == h.Period && timeRemain > h.TimeLeft {
		timeRemain = h.TimeLeft
	}

	h.HomeScore = homeScore
	h.AwayScore = awayScore
	h.Period = period
	h.TimeLeft = timeRemain
	h.hasLiveData = true

	return firstUpdate || scoreChanged
}

func (h *HockeyState) CheckScoreDrop(homeScore, awayScore int, confirmSec int) string {
	prevTotal := h.HomeScore + h.AwayScore
	newTotal := homeScore + awayScore

	if newTotal >= prevTotal {
		if h.scoreDropPending {
			h.ClearScoreDropPending()
		}
		return "accept"
	}

	now := time.Now()
	if h.scoreDropData != nil {
		if homeScore == h.scoreDropData.homeScore && awayScore == h.scoreDropData.awayScore {
			if now.Sub(h.scoreDropData.firstSeen) >= time.Duration(confirmSec)*time.Second {
				h.ClearScoreDropPending()
				return "confirmed"
			}
		} else {
			h.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: homeScore, awayScore: awayScore}
		}
		h.scoreDropPending = true
		return "pending"
	}

	h.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: homeScore, awayScore: awayScore}
	h.scoreDropPending = true
	return "new_drop"
}

func (h *HockeyState) ClearScoreDropPending() {
	h.scoreDropPending = false
	h.scoreDropData = nil
}

func (h *HockeyState) IsScoreDropPending() bool { return h.scoreDropPending }

func (h *HockeyState) HasOrdered(side string) bool {
	return h.orderedSides[scoreKey{side: side, homeScore: h.HomeScore, awayScore: h.AwayScore}]
}

func (h *HockeyState) MarkOrdered(side string) {
	h.orderedSides[scoreKey{side: side, homeScore: h.HomeScore, awayScore: h.AwayScore}] = true
}

func (h *HockeyState) ClearOrdered() {
	h.orderedSides = make(map[scoreKey]bool)
}

func (h *HockeyState) SetTickers(home, away, _ string) {
	h.HomeTicker = home
	h.AwayTicker = away
}
