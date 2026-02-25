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
	Updated   string                  `json:"updated"`
	UpdatedTS int64                   `json:"updated_ts"`
	Events    map[string]WebhookEvent `json:"events"`
}

// WebhookEvent is a single game within the webhook payload.
type WebhookEvent struct {
	Info     EventInfo             `json:"info"`
	TeamInfo TeamInfo              `json:"team_info"`
	Odds     map[string]OddsMarket `json:"odds"`
	Core     map[string]string     `json:"core"`
	Stats    map[string]any        `json:"stats"`
	STS      string                `json:"sts"`
}

type EventInfo struct {
	Name       string `json:"name"`
	Period     string `json:"period"`
	Status     string `json:"status"`
	Minute     string `json:"minute"`
	Seconds    string `json:"seconds"`
	League     string `json:"league"`
	Category   string `json:"category"`
	Events     []any  `json:"events"`
	StartTsUTC string `json:"start_ts_utc"`
	StartTime  string `json:"start_time"`
	StartDate  string `json:"start_date"`
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
// GameUpdateEvent domain events.
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
		homeRC, awayRC := p.extractRedCards(&ev)

		gu := events.GameUpdateEvent{
			EID:          eid,
			Source:       "goalserve_webhook",
			Sport:        p.sport,
			League:       league,
			HomeTeam:     strings.TrimSpace(ev.TeamInfo.Home.Name),
			AwayTeam:     strings.TrimSpace(ev.TeamInfo.Away.Name),
			HomeScore:    homeScore,
			AwayScore:    awayScore,
			Period:       period,
			TimeLeft:     p.calcTimeRemaining(period, ev.Info.Minute, ev.Info.Seconds),
			GameStartUTC: parseStartTsUTC(ev.Info.StartTsUTC),
			HomeRedCards: homeRC,
			AwayRedCards: awayRC,
		}
		if odds != nil {
			gu.HomeStrength = odds.HomePregameStrength
			gu.DrawPct = odds.DrawPct
			gu.AwayStrength = odds.AwayPregameStrength
		}

		gu.MatchStatus = p.inferMatchStatus(&ev, homeScore, awayScore, period)

		if p.sport == events.SportHockey {
			gu.PowerPlay, gu.HomePenaltyCount, gu.AwayPenaltyCount = p.extractPowerPlay(&ev)
		}

		out = append(out, events.Event{
			ID:        eid,
			Type:      events.EventGameUpdate,
			Sport:     p.sport,
			League:    league,
			GameID:    eid,
			Timestamp: now,
			Payload:   gu,
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

// calcTimeRemaining computes minutes remaining from the period string plus
// the Minute field (soccer: elapsed minutes) or Seconds field (hockey: period clock).
func (p *Parser) calcTimeRemaining(period, minute, seconds string) float64 {
	low := strings.ToLower(strings.TrimSpace(period))
	switch p.sport {
	case events.SportSoccer:
		return soccerTimeRemaining(low, minute)
	case events.SportHockey:
		return hockeyTimeRemaining(low, seconds)
	case events.SportFootball:
		return footballTimeRemaining(low)
	}
	return 0
}

// soccerTimeRemaining parses the GoalServe minute field (elapsed minutes)
// and returns regulation minutes remaining. Handles formats: "34", "45+2", "67:23".
func soccerTimeRemaining(period, minute string) float64 {
	switch {
	case strings.Contains(period, "finished") || period == "ft" || period == "ended":
		return 0
	case strings.Contains(period, "half time") || period == "ht" || period == "halftime":
		return 45
	case strings.Contains(period, "extra") || period == "et":
		return 0
	case strings.Contains(period, "penalties") || period == "pen":
		return 0
	}

	m := strings.TrimSpace(minute)
	if m != "" {
		elapsed := parseTimer(m)
		remain := 90.0 - elapsed
		if remain < 0 {
			remain = 0
		}
		return remain
	}

	if strings.Contains(period, "1st half") || strings.Contains(period, "1st") {
		return 45
	}
	if strings.Contains(period, "2nd half") || strings.Contains(period, "2nd") {
		return 45
	}
	if period == "not started" || period == "" {
		return 90
	}
	return 90
}

// hockeyTimeRemaining parses the period string and the Seconds field
// (MM:SS countdown clock within the current period) to compute total minutes remaining.
func hockeyTimeRemaining(period, seconds string) float64 {
	periodMins := parsePeriodClock(seconds, 20.0)

	switch {
	case strings.Contains(period, "1st"):
		return periodMins + 40
	case strings.Contains(period, "2nd"):
		return periodMins + 20
	case strings.Contains(period, "3rd"):
		return periodMins
	case strings.Contains(period, "OVERTIME") || period == "ot":
		return 5
	case strings.Contains(period, "shootout") || strings.Contains(period, "penalties"):
		return 0
	}
	return 60
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
	case strings.Contains(period, "OVERTIME") || period == "ot":
		return 0
	default:
		return 60
	}
}

// parseTimer parses the GoalServe timer string (elapsed minutes) into a float.
// Handles: "34", "45+2" (stoppage), "67:23" (min:sec).
func parseTimer(timer string) float64 {
	t := strings.TrimSpace(timer)
	if t == "" {
		return 0
	}

	if strings.Contains(t, "+") {
		parts := strings.SplitN(t, "+", 2)
		base, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		added, err2 := strconv.ParseFloat(strings.TrimSpace(strings.Split(parts[1], ":")[0]), 64)
		if err1 == nil && err2 == nil {
			return base + added
		}
	}

	if strings.Contains(t, ":") {
		parts := strings.SplitN(t, ":", 2)
		mins, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err == nil {
			return mins
		}
	}

	if v, err := strconv.ParseFloat(t, 64); err == nil {
		return v
	}
	return 0
}

// parsePeriodClock parses a "MM:SS" countdown clock string into minutes remaining
// in the current period. Returns defaultVal if unparseable.
func parsePeriodClock(seconds string, defaultVal float64) float64 {
	s := strings.TrimSpace(seconds)
	if s == "" {
		return defaultVal
	}
	if strings.Contains(s, ":") {
		parts := strings.SplitN(s, ":", 2)
		mins, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		secs, err2 := strconv.Atoi(strings.TrimSpace(parts[1][:min(2, len(parts[1]))]))
		if err1 == nil && err2 == nil {
			return float64(mins) + float64(secs)/60.0
		}
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
		return v
	}
	return defaultVal
}

// ParsedOdds holds vig-free implied probabilities extracted from a webhook event.
type ParsedOdds struct {
	HomePregameStrength *float64
	DrawPct             *float64 // nil for hockey
	AwayPregameStrength *float64
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
		"extra time", "OVERTIME",
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
			HomePregameStrength: &homeProb,
			AwayPregameStrength: &awayProb,
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
		HomePregameStrength: &h,
		DrawPct:             &d,
		AwayPregameStrength: &a,
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

func parseStartTsUTC(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	if v > 1e12 {
		v /= 1000 // milliseconds → seconds
	}
	return v
}

// extractRedCards pulls red-card counts from the webhook event.
// GoalServe sends these in Stats (as numeric strings or numbers) or Core.
func (p *Parser) extractRedCards(ev *WebhookEvent) (home, away int) {
	if p.sport != events.SportSoccer {
		return 0, 0
	}

	home = intFromMap(ev.Stats, "redcards_home", "red_cards_home", "home_redcards")
	away = intFromMap(ev.Stats, "redcards_away", "red_cards_away", "away_redcards")

	if home == 0 && away == 0 {
		home = intFromStringMap(ev.Core, "redcards_home", "red_cards_home", "home_redcards")
		away = intFromStringMap(ev.Core, "redcards_away", "red_cards_away", "away_redcards")
	}

	return home, away
}

// intFromMap tries several keys in a map[string]any and returns the first
// value that can be interpreted as a non-zero int.
func intFromMap(m map[string]any, keys ...string) int {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch val := v.(type) {
		case float64:
			return int(val)
		case string:
			if n, err := strconv.Atoi(val); err == nil {
				return n
			}
		}
	}
	return 0
}

// intFromStringMap tries several keys in a map[string]string.
func intFromStringMap(m map[string]string, keys ...string) int {
	for _, k := range keys {
		if s, ok := m[k]; ok {
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
		}
	}
	return 0
}

// inferMatchStatus determines the match status from a single webhook snapshot.
// Detects GAME START (0-0, first period, early clock) and OVERTIME (hockey).
// SCORE CHANGE is determined by the engine via state diffing.
func (p *Parser) inferMatchStatus(ev *WebhookEvent, homeScore, awayScore int, period string) events.MatchStatus {
	low := strings.ToLower(strings.TrimSpace(period))

	if homeScore == 0 && awayScore == 0 {
		switch p.sport {
		case events.SportSoccer:
			if strings.Contains(low, "1st") {
				if m, err := strconv.Atoi(strings.Split(ev.Info.Minute, ":")[0]); err == nil && m <= 5 {
					return events.StatusGameStart
				}
			}
		case events.SportHockey:
			if strings.Contains(low, "1st") {
				if mins := parsePeriodClock(ev.Info.Seconds, 0); mins >= 17 {
					return events.StatusGameStart
				}
			}
		}
	}

	if p.sport == events.SportHockey {
		if strings.Contains(low, "overtime") || low == "ot" {
			return events.StatusOvertime
		}
	}

	return events.StatusLive
}

// extractPowerPlay parses the hockey STS field for power play state and
// cumulative penalty counts. The STS format from GoalServe looks like:
//
//	Penalties=3:4|Goals on Power Play=0:0|GPP=0 / 3:0 / 4|INFO=5 ON 4|
//
// Returns whether a PP is active and cumulative home:away penalty counts.
func (p *Parser) extractPowerPlay(ev *WebhookEvent) (powerPlay bool, homePen, awayPen int) {
	if ev.STS == "" {
		return false, 0, 0
	}
	upper := strings.ToUpper(ev.STS)

	// Check INFO= for active power play (5 ON 4, 5 ON 3, 4 ON 3).
	if idx := strings.Index(upper, "INFO="); idx >= 0 {
		info := upper[idx+5:]
		if i := strings.Index(info, "|"); i >= 0 {
			info = info[:i]
		}
		powerPlay = strings.Contains(info, "5 ON 4") ||
			strings.Contains(info, "5 ON 3") ||
			strings.Contains(info, "4 ON 3")
	}

	// Parse Penalties=H:A for cumulative counts.
	if idx := strings.Index(upper, "PENALTIES="); idx >= 0 {
		rest := upper[idx+len("PENALTIES="):]
		if i := strings.Index(rest, "|"); i >= 0 {
			rest = rest[:i]
		}
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			if h, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				homePen = h
			}
			if a, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
				awayPen = a
			}
		}
	}

	return powerPlay, homePen, awayPen
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
