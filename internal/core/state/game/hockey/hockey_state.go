package hockey

import (
	"strings"

	game "github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/events"
)

// HockeyState holds the LIVE state for a single hockey game.
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

	HomeStrength float64 // pregame strength (0–1)
	AwayStrength float64

	HomeTicker string
	AwayTicker string
	SeriesSlug string
	SeriesName string

	ModelHomePct float64 // 0–100
	ModelAwayPct float64 // 0–100

	// Power play state. Updated from GoalServe STS field every webhook.
	IsHomePowerPlay  bool
	IsAwayPowerPlay  bool
	HomePenaltyCount int // cumulative, from STS Penalties= field
	AwayPenaltyCount int

	// LastPowerPlayWasHome is set when a PP ends (before clearing flags) so the
	// display can show who had the PP. Cleared when the next PP starts.
	LastPowerPlayWasHome *bool

	PregameApplied bool
	PregameG0      *float64 // expected total goals from O/U market, nil if unavailable

	EdgeHomeYes float64
	EdgeAwayYes float64
	EdgeHomeNo  float64
	EdgeAwayNo  float64

	game.ScoreDropTracker

	OVERTIMENotified bool

	hasLIVEData    bool
	finaled        bool
	shootoutLogged bool
}

func New(eid, league, homeTeam, awayTeam string) *HockeyState {
	return &HockeyState{
		EID:          eid,
		League:       league,
		HomeTeam:     homeTeam,
		AwayTeam:     awayTeam,
		HomeStrength: 0,
		AwayStrength: 0,
		TimeLeft:     999,
		ModelHomePct: 100,
		ModelAwayPct: 100,
	}
}

func (h *HockeyState) GetEID() string            { return h.EID }
func (h *HockeyState) GetHomeTeam() string       { return h.HomeTeam }
func (h *HockeyState) GetAwayTeam() string       { return h.AwayTeam }
func (h *HockeyState) GetHomeScore() int         { return h.HomeScore }
func (h *HockeyState) GetAwayScore() int         { return h.AwayScore }
func (h *HockeyState) GetPeriod() string         { return h.Period }
func (h *HockeyState) GetTimeRemaining() float64 { return h.TimeLeft }
func (h *HockeyState) HasLIVEData() bool         { return h.hasLIVEData }
func (h *HockeyState) HasPregame() bool          { return h.PregameApplied }

func (h *HockeyState) DeduplicateStatus(status events.MatchStatus) events.MatchStatus {
	if status != events.StatusOvertime {
		return status
	}
	if h.OVERTIMENotified {
		return events.StatusLive
	}
	h.OVERTIMENotified = true
	return status
}

func (h *HockeyState) Lead() int { return h.HomeScore - h.AwayScore }

func (h *HockeyState) IsOVERTIME() bool {
	p := strings.ToLower(strings.TrimSpace(h.Period))
	return strings.Contains(p, "overtime") || p == "ot" || p == "penalties" || p == "shootout"
}

func (h *HockeyState) IsFinished() bool {
	p := strings.ToLower(strings.TrimSpace(h.Period))
	return p == "finished" || p == "final" || p == "ended" ||
		strings.Contains(p, "after overtime") ||
		strings.Contains(p, "after ot")
}

func (h *HockeyState) IsLIVE() bool {
	return h.Period != "" && !h.IsFinished()
}

func (h *HockeyState) UpdateGameState(homeScore, awayScore int, period string, timeRemain float64) bool {
	firstUpdate := !h.hasLIVEData
	scoreChanged := h.HomeScore != homeScore || h.AwayScore != awayScore

	h.HomeScore = homeScore
	h.AwayScore = awayScore
	h.Period = period
	h.TimeLeft = timeRemain
	h.hasLIVEData = true

	return firstUpdate || scoreChanged
}

func (h *HockeyState) CheckScoreDrop(homeScore, awayScore int, confirmSec int) string {
	return h.ScoreDropTracker.CheckDrop(h.HomeScore, h.AwayScore, homeScore, awayScore, confirmSec)
}

func (h *HockeyState) SetTickers(home, away, _ string) {
	h.HomeTicker = home
	h.AwayTicker = away
}

func (h *HockeyState) SetIdentifiers(eid, league string) {
	h.EID = eid
	h.League = league
}

// SetPregame writes pregame odds directly. Called by the engine during
// initialization — the pregame HTTP is the source of truth for orientation.
func (h *HockeyState) SetPregame(home, away, _ float64, g0 float64) {
	h.HomeStrength = home
	h.AwayStrength = away
	if g0 > 0 {
		h.PregameG0 = &g0
	}
	h.PregameApplied = true
}

func (h *HockeyState) RecalcEdge(tickers map[string]*game.TickerData) {
	if h.ModelHomePct == 0 && h.ModelAwayPct == 0 {
		return
	}
	h.EdgeHomeYes = edgeFor(h.ModelHomePct, yesAsk(tickers, h.HomeTicker))
	h.EdgeAwayYes = edgeFor(h.ModelAwayPct, yesAsk(tickers, h.AwayTicker))
	h.EdgeHomeNo = edgeFor(100-h.ModelHomePct, noAsk(tickers, h.HomeTicker))
	h.EdgeAwayNo = edgeFor(100-h.ModelAwayPct, noAsk(tickers, h.AwayTicker))
}

func (h *HockeyState) HasSignificantEdge() bool {
	t := game.EdgeThresholdPct()
	for _, e := range []float64{
		h.EdgeHomeYes, h.EdgeAwayYes,
		h.EdgeHomeNo, h.EdgeAwayNo,
	} {
		if e >= t {
			return true
		}
	}
	return false
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
