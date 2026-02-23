package hockey

import (
	"strings"

	game "github.com/charleschow/hft-trading/internal/core/state/game"
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
	PinnacleUpdated bool     // true when Pinnacle odds changed in the latest Evaluate

	// Power play state. Updated from GoalServe STS field every webhook.
	IsHomePowerPlay  bool
	IsAwayPowerPlay  bool
	HomePenaltyCount int // cumulative, from STS Penalties= field
	AwayPenaltyCount int

	PregameApplied bool
	PregameG0      *float64 // expected total goals from O/U market, nil if unavailable

	EdgeHomeYes float64
	EdgeAwayYes float64
	EdgeHomeNo  float64
	EdgeAwayNo  float64

	game.ScoreDropTracker

	OvertimeNotified bool

	hasLiveData    bool
	orderedSides   map[scoreKey]bool
	finaled        bool
	shootoutLogged bool
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
		HomeWinPct:   0,
		AwayWinPct:   0,
		TimeLeft:     999,
		ModelHomePct: 100,
		ModelAwayPct: 100,
		orderedSides: make(map[scoreKey]bool),
	}
}

func (h *HockeyState) GetEID() string            { return h.EID }
func (h *HockeyState) GetHomeTeam() string       { return h.HomeTeam }
func (h *HockeyState) GetAwayTeam() string       { return h.AwayTeam }
func (h *HockeyState) GetHomeScore() int         { return h.HomeScore }
func (h *HockeyState) GetAwayScore() int         { return h.AwayScore }
func (h *HockeyState) GetPeriod() string         { return h.Period }
func (h *HockeyState) GetTimeRemaining() float64 { return h.TimeLeft }
func (h *HockeyState) HasLiveData() bool  { return h.hasLiveData }
func (h *HockeyState) HasPregame() bool   { return h.PregameApplied }

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
	return h.ScoreDropTracker.CheckDrop(h.HomeScore, h.AwayScore, homeScore, awayScore, confirmSec)
}

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

func (h *HockeyState) RecalcEdge(tickers map[string]*game.TickerData) {
	if h.ModelHomePct == 0 && h.ModelAwayPct == 0 {
		return
	}
	h.EdgeHomeYes = edgeFor(h.ModelHomePct, yesAsk(tickers, h.HomeTicker))
	h.EdgeAwayYes = edgeFor(h.ModelAwayPct, yesAsk(tickers, h.AwayTicker))
	h.EdgeHomeNo = edgeFor(120-h.ModelHomePct, noAsk(tickers, h.HomeTicker))
	h.EdgeAwayNo = edgeFor(100-h.ModelAwayPct, noAsk(tickers, h.AwayTicker))
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
