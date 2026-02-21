package goalserve_webhook

import (
	"strconv"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// WebhookPayload is the top-level JSON envelope from GoalServe.
//
//	{
//	  "updated": "2026-02-20T...",
//	  "updated_ts": 1740000000,
//	  "events": { "<eid>": { "info": {...}, "team_info": {...}, "odds": {...} } }
//	}
type WebhookPayload struct {
	Updated   string                    `json:"updated"`
	UpdatedTS int64                     `json:"updated_ts"`
	Events    map[string]WebhookEvent   `json:"events"`
}

// WebhookEvent is a single game within the webhook payload.
type WebhookEvent struct {
	Info     EventInfo                  `json:"info"`
	TeamInfo TeamInfo                   `json:"team_info"`
	Odds     map[string]OddsMarket      `json:"odds"`
	Core     map[string]string          `json:"core"`
	Stats    map[string]any             `json:"stats"`
}

type EventInfo struct {
	Name     string `json:"name"`
	Period   string `json:"period"`
	Status   string `json:"status"`
	Timer    string `json:"timer"`
	Seconds  string `json:"seconds"`
	League   string `json:"league"`
	Category string `json:"category"`
	Events   []any  `json:"events"`
}

type TeamInfo struct {
	Home TeamDetail `json:"home"`
	Away TeamDetail `json:"away"`
}

type TeamDetail struct {
	Name  string `json:"name"`
	Score string `json:"score"`
	Goals string `json:"goals"` // soccer sometimes uses "goals" instead of "score"
}

type OddsMarket struct {
	Name         string                     `json:"name"`
	Suspend      string                     `json:"suspend"`
	Participants map[string]OddsParticipant `json:"participants"`
}

type OddsParticipant struct {
	Name    string `json:"name"`
	ValueEU string `json:"value_eu"`
	Suspend string `json:"suspend"`
}

// Parser converts raw webhook payloads into domain events.
type Parser struct {
	sport events.Sport
}

func NewParser(sport events.Sport) *Parser {
	return &Parser{sport: sport}
}

// Parse walks through every event in the payload and emits
// ScoreChangeEvent / GameFinishEvent domain events.
func (p *Parser) Parse(payload *WebhookPayload) []events.Event {
	if payload == nil || len(payload.Events) == 0 {
		return nil
	}

	var out []events.Event
	now := time.Now()

	for eid, ev := range payload.Events {
		name := strings.TrimSpace(ev.Info.Name)
		if name == "" {
			continue
		}

		homeScore, awayScore, ok := p.extractScores(&ev)
		if !ok {
			continue
		}

		period := p.normalizePeriod(&ev)
		league := ev.Info.League

		odds := p.parseOdds(&ev)

		if p.isFinished(period) {
			finishEvt := events.GameFinishEvent{
				EID:        eid,
				Sport:      p.sport,
				League:     league,
				HomeTeam:   strings.TrimSpace(ev.TeamInfo.Home.Name),
				AwayTeam:   strings.TrimSpace(ev.TeamInfo.Away.Name),
				HomeScore:  homeScore,
				AwayScore:  awayScore,
				FinalState: period,
			}
			out = append(out, events.Event{
				ID:        eid,
				Type:      events.EventGameFinish,
				Sport:     p.sport,
				League:    league,
				GameID:    eid,
				Timestamp: now,
				Payload:   finishEvt,
			})
			continue
		}

		scoreEvt := events.ScoreChangeEvent{
			EID:       eid,
			Sport:     p.sport,
			League:    league,
			HomeTeam:  strings.TrimSpace(ev.TeamInfo.Home.Name),
			AwayTeam:  strings.TrimSpace(ev.TeamInfo.Away.Name),
			HomeScore: homeScore,
			AwayScore: awayScore,
			Period:    period,
			TimeLeft:  p.estimateTimeRemaining(period),
		}
		if odds != nil {
			scoreEvt.HomeWinPct = odds.HomeWinPct
			scoreEvt.DrawPct = odds.DrawPct
			scoreEvt.AwayWinPct = odds.AwayWinPct
		}

		out = append(out, events.Event{
			ID:        eid,
			Type:      events.EventScoreChange,
			Sport:     p.sport,
			League:    league,
			GameID:    eid,
			Timestamp: now,
			Payload:   scoreEvt,
		})

		telemetry.Metrics.EventsProcessed.Inc()
	}

	return out
}

func (p *Parser) extractScores(ev *WebhookEvent) (home, away int, ok bool) {
	rawHome := ev.TeamInfo.Home.Score
	if rawHome == "" {
		rawHome = ev.TeamInfo.Home.Goals
	}
	rawAway := ev.TeamInfo.Away.Score
	if rawAway == "" {
		rawAway = ev.TeamInfo.Away.Goals
	}
	if rawHome == "" || rawAway == "" {
		return 0, 0, false
	}

	h, err1 := strconv.Atoi(rawHome)
	a, err2 := strconv.Atoi(rawAway)
	if err1 != nil || err2 != nil {
		telemetry.Warnf("goalserve: non-numeric score home=%q away=%q", rawHome, rawAway)
		return 0, 0, false
	}
	return h, a, true
}

func (p *Parser) normalizePeriod(ev *WebhookEvent) string {
	period := ev.Info.Period
	if period == "" {
		period = ev.Info.Status
	}
	return strings.TrimSpace(period)
}

func (p *Parser) isFinished(period string) bool {
	low := strings.ToLower(period)
	finishedTokens := []string{
		"finished", "final", "ended", "ft",
		"after over time", "after overtime", "after ot",
		"after extra time", "aet",
		"after penalties", "after pen",
	}
	for _, tok := range finishedTokens {
		if low == tok || strings.Contains(low, tok) {
			return true
		}
	}
	return false
}

// estimateTimeRemaining maps period strings to approximate minutes remaining.
func (p *Parser) estimateTimeRemaining(period string) float64 {
	low := strings.ToLower(strings.TrimSpace(period))
	switch p.sport {
	case events.SportHockey:
		return hockeyTimeRemaining(low)
	case events.SportSoccer:
		return soccerTimeRemaining(low)
	case events.SportFootball:
		return footballTimeRemaining(low)
	}
	return 0
}

func hockeyTimeRemaining(period string) float64 {
	switch {
	case strings.Contains(period, "1st"):
		return 40
	case strings.Contains(period, "2nd"):
		return 20
	case strings.Contains(period, "3rd"):
		return 0
	case strings.Contains(period, "overtime") || period == "ot":
		return 0
	default:
		return 60
	}
}

func soccerTimeRemaining(period string) float64 {
	switch {
	case strings.Contains(period, "1st half"):
		return 45
	case strings.Contains(period, "half time") || period == "ht":
		return 45
	case strings.Contains(period, "2nd half"):
		return 0
	case strings.Contains(period, "extra"):
		return 0
	default:
		return 90
	}
}

func footballTimeRemaining(period string) float64 {
	switch {
	case strings.Contains(period, "q1") || strings.Contains(period, "1st quarter"):
		return 45
	case strings.Contains(period, "q2") || strings.Contains(period, "2nd quarter"):
		return 30
	case strings.Contains(period, "halftime") || strings.Contains(period, "half"):
		return 30
	case strings.Contains(period, "q3") || strings.Contains(period, "3rd quarter"):
		return 15
	case strings.Contains(period, "q4") || strings.Contains(period, "4th quarter"):
		return 0
	case strings.Contains(period, "overtime") || period == "ot":
		return 0
	default:
		return 60
	}
}

// ParsedOdds holds vig-free implied probabilities extracted from a webhook event.
type ParsedOdds struct {
	HomeWinPct *float64
	DrawPct    *float64 // nil for hockey
	AwayWinPct *float64
}

// parseOdds extracts vig-free moneyline probabilities from the event's odds map.
// Mirrors goalserve/inplay.py parse_event_odds.
func (p *Parser) parseOdds(ev *WebhookEvent) *ParsedOdds {
	if len(ev.Odds) == 0 {
		return nil
	}

	hockeyKW := []string{"home/away", "moneyline"}
	soccerKW := []string{"moneyline", "match winner", "fulltime", "full time", "1x2"}
	excludeKW := []string{
		"1st half", "2nd half", "first half", "second half",
		"1st period", "2nd period", "3rd period",
		"half time", "halftime",
		"minute", "corner", "card", "handicap",
		"extra time", "overtime",
	}

	var mlMarket *OddsMarket
	for _, mkt := range ev.Odds {
		name := strings.ToLower(mkt.Name)
		if len(mkt.Participants) == 0 {
			continue
		}

		kw := soccerKW
		if p.sport == events.SportHockey {
			kw = hockeyKW
		}

		if mlMarket == nil && containsAny(name, kw) && !containsAny(name, excludeKW) {
			m := mkt
			mlMarket = &m
		}
	}

	if mlMarket == nil || mlMarket.Suspend == "1" {
		return nil
	}

	decOdds := make(map[string]float64)
	for _, participant := range mlMarket.Participants {
		if participant.Suspend == "1" {
			continue
		}
		pName := strings.ToLower(strings.TrimSpace(participant.Name))
		val, err := strconv.ParseFloat(participant.ValueEU, 64)
		if err != nil || val <= 1.0 {
			continue
		}
		decOdds[pName] = val
	}

	if p.sport == events.SportHockey || p.sport == events.SportFootball {
		homeDec, hOK := decOdds["home"]
		awayDec, aOK := decOdds["away"]
		if !hOK || !aOK {
			return nil
		}
		homeProb, awayProb := removeVig2(homeDec, awayDec)
		return &ParsedOdds{
			HomeWinPct: &homeProb,
			AwayWinPct: &awayProb,
		}
	}

	// Soccer: 3-way
	homeDec, hOK := decOdds["home"]
	drawDec, dOK := decOdds["draw"]
	awayDec, aOK := decOdds["away"]
	if !hOK || !dOK || !aOK {
		return nil
	}
	h, d, a := removeVig3(homeDec, drawDec, awayDec)
	return &ParsedOdds{
		HomeWinPct: &h,
		DrawPct:    &d,
		AwayWinPct: &a,
	}
}

// removeVig2 removes the bookmaker margin from 2-way decimal odds.
func removeVig2(a, b float64) (float64, float64) {
	rawA := 1.0 / a
	rawB := 1.0 / b
	total := rawA + rawB
	return rawA / total, rawB / total
}

// removeVig3 removes the bookmaker margin from 3-way decimal odds.
func removeVig3(a, b, c float64) (float64, float64, float64) {
	rawA := 1.0 / a
	rawB := 1.0 / b
	rawC := 1.0 / c
	total := rawA + rawB + rawC
	return rawA / total, rawB / total, rawC / total
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
