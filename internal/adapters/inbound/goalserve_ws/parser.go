package goalserve_ws

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// ParseUpdt converts a GoalServe WebSocket "updt" message into a domain event.
func ParseUpdt(msg *UpdtMessage) *events.Event {
	sport := mapSport(msg.Sport)
	if sport == "" {
		return nil
	}

	homeScore, awayScore, ok := extractScores(sport, msg)
	if !ok {
		return nil
	}

	period := mapPeriod(sport, msg.PC)
	if msg.SC != "" && finishStateCodes[msg.SC] {
		period = "Finished"
	}
	timeLeft := calcTimeRemaining(sport, msg.PC, msg.ET)

	gu := events.GameUpdateEvent{
		EID:          msg.ID,
		Source:       "goalserve_ws",
		Sport:        sport,
		League:       msg.CmpName,
		HomeTeam:     msg.T1.Name,
		AwayTeam:     msg.T2.Name,
		HomeScore:    homeScore,
		AwayScore:    awayScore,
		Period:       period,
		TimeLeft:     timeLeft,
		GameStartUTC: parseStartTime(msg.ST),
	}

	if sport == events.SportSoccer {
		gu.HomeRedCards, gu.AwayRedCards = extractRedCards(msg)
	}
	if sport == events.SportHockey {
		gu.PowerPlay, gu.HomePenaltyCount, gu.AwayPenaltyCount = extractPowerPlay(msg)
	}

	odds := extractMoneylineOdds(sport, msg)
	if odds != nil {
		gu.HomeStrength = odds.home
		gu.DrawPct = odds.draw
		gu.AwayStrength = odds.away
	}

	gu.MatchStatus = inferMatchStatus(sport, msg, homeScore, awayScore)

	// Log state codes for future mapping (log-and-learn).
	telemetry.Debugf("goalserve_ws: sc=%s pc=%d et=%ds sport=%s id=%s %s vs %s %d-%d",
		msg.SC, msg.PC, msg.ET, msg.Sport, msg.ID, msg.T1.Name, msg.T2.Name, homeScore, awayScore)

	evt := events.Event{
		ID:        msg.ID,
		Type:      events.EventGameUpdate,
		Sport:     sport,
		League:    msg.CmpName,
		GameID:    msg.ID,
		Timestamp: time.Now(),
		Payload:   gu,
	}
	return &evt
}

func mapSport(sp string) events.Sport {
	switch sp {
	case "soccer":
		return events.SportSoccer
	case "hockey":
		return events.SportHockey
	case "amfootball":
		return events.SportFootball
	default:
		return ""
	}
}

// GoalServe WS sport type strings for connection URLs.
func InternalToWSSport(sport events.Sport) string {
	switch sport {
	case events.SportSoccer:
		return "soccer"
	case events.SportHockey:
		return "hockey"
	case events.SportFootball:
		return "amfootball"
	default:
		return string(sport)
	}
}

// extractScores reads the stats map to get home/away scores.
// Soccer uses stats.a (goals), hockey uses stats.T (total).
func extractScores(sport events.Sport, msg *UpdtMessage) (home, away int, ok bool) {
	switch sport {
	case events.SportSoccer, events.SportFootball:
		return statPair(msg.Stats, "a")
	case events.SportHockey:
		return statPair(msg.Stats, "T")
	}
	return 0, 0, false
}

// statPair extracts a [home, away] int pair from the stats map.
func statPair(stats map[string]json.RawMessage, key string) (int, int, bool) {
	raw, exists := stats[key]
	if !exists {
		return 0, 0, false
	}
	var pair [2]int
	if err := json.Unmarshal(raw, &pair); err != nil {
		return 0, 0, false
	}
	return pair[0], pair[1], true
}

// mapPeriod converts numeric period code to a human-readable string
// that the strategy engine expects.
func mapPeriod(sport events.Sport, pc int) string {
	switch sport {
	case events.SportSoccer:
		switch pc {
		case 0:
			return "Not Started"
		case 1:
			return "1st Half"
		case 2:
			return "Half Time"
		case 3:
			return "2nd Half"
		case 4:
			return "Extra Time 1st Half"
		case 5:
			return "Extra Time 2nd Half"
		case 6:
			return "Penalties"
		case 255:
			return "Finished"
		default:
			telemetry.Warnf("goalserve_ws: unmapped soccer pc=%d", pc)
			return fmt.Sprintf("Period %d", pc)
		}
	case events.SportHockey:
		switch pc {
		case 0:
			return "Not Started"
		case 1:
			return "1st Period"
		case 2:
			return "2nd Period"
		case 3:
			return "3rd Period"
		case 4:
			return "OVERTIME"
		case 5:
			return "Shootout"
		case 6:
			return "1st Intermission"
		case 7:
			return "2nd Intermission"
		case 8:
			return "OT Intermission"
		case 255:
			return "Finished"
		default:
			telemetry.Warnf("goalserve_ws: unmapped hockey pc=%d, treating as break", pc)
			return fmt.Sprintf("Period %d", pc)
		}
	case events.SportFootball:
		switch pc {
		case 0:
			return "Not Started"
		case 1:
			return "Q1"
		case 2:
			return "Q2"
		case 3:
			return "Halftime"
		case 4:
			return "Q3"
		case 5:
			return "Q4"
		case 6:
			return "OVERTIME"
		case 255:
			return "Finished"
		default:
			telemetry.Warnf("goalserve_ws: unmapped football pc=%d", pc)
			return fmt.Sprintf("Period %d", pc)
		}
	}
	return fmt.Sprintf("Period %d", pc)
}

// calcTimeRemaining computes minutes remaining from period code and elapsed seconds.
// et is total elapsed seconds since kickoff (not period-relative).
func calcTimeRemaining(sport events.Sport, pc, et int) float64 {
	switch sport {
	case events.SportSoccer:
		elapsed := float64(et) / 60.0
		switch pc {
		case 0:
			return 90.0
		case 1:
			remain := 90.0 - elapsed
			if remain < 0 {
				remain = 0
			}
			return remain
		case 2:
			return 45.0
		case 3:
			remain := 90.0 - elapsed
			if remain < 0 {
				remain = 0
			}
			return remain
		default:
			return 0
		}
	case events.SportHockey:
		totalGame := 60.0 // 3 x 20 min periods
		remain := totalGame - float64(et)/60.0
		switch pc {
		case 0:
			return 60.0
		case 1, 2, 3:
			if remain < 0 {
				remain = 0
			}
			return remain
		default:
			return 0
		}
	case events.SportFootball:
		totalGame := 60.0 // 4 x 15 min quarters
		remain := totalGame - float64(et)/60.0
		switch pc {
		case 0:
			return 60.0
		case 1, 2, 4, 5:
			if remain < 0 {
				remain = 0
			}
			return remain
		default:
			return 0
		}
	}
	return 0
}

func parseStartTime(st json.Number) int64 {
	s := st.String()
	if s == "" || s == "null" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	if v > 1e12 {
		v /= 1000
	}
	return v
}

func extractRedCards(msg *UpdtMessage) (home, away int) {
	h, a, ok := statPair(msg.Stats, "r")
	if !ok {
		return 0, 0
	}
	return h, a
}

// extractPowerPlay parses the cms (comments) array for active power play state.
// mt="125" = PP start ("5 on 4"), mt="129" = PP over, mt="128" = 4 on 4.
// Also extracts penalty counts from the stat string.
func extractPowerPlay(msg *UpdtMessage) (powerPlay bool, homePen, awayPen int) {
	// Parse penalty counts from stat string: "Penalties=H:A|..."
	if msg.Stat != "" {
		upper := strings.ToUpper(msg.Stat)
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
	}

	// Detect active PP from cms events by looking at the latest PP-related comment.
	// Walk in reverse to find the most recent PP event.
	for i := len(msg.CMS) - 1; i >= 0; i-- {
		c := msg.CMS[i]
		switch c.MT {
		case "125": // PP start (e.g. "5 on 4")
			powerPlay = true
			return
		case "129": // PP over
			powerPlay = false
			return
		case "128": // 4 on 4
			powerPlay = false
			return
		}
	}

	return false, homePen, awayPen
}

type parsedOdds struct {
	home *float64
	draw *float64
	away *float64
}

// extractMoneylineOdds finds the fulltime moneyline market heuristically:
// soccer: 3-way (1/X/2) without handicap; hockey/football: 2-way (1/2) without handicap.
func extractMoneylineOdds(sport events.Sport, msg *UpdtMessage) *parsedOdds {
	if len(msg.Odds) == 0 {
		return nil
	}

	wantWays := 3
	if sport == events.SportHockey || sport == events.SportFootball {
		wantWays = 2
	}

	for _, mkt := range msg.Odds {
		if mkt.BL != 0 || mkt.HA != nil {
			continue
		}
		if len(mkt.O) != wantWays {
			continue
		}

		allUnblocked := true
		for _, o := range mkt.O {
			if o.Blocked != 0 || o.Value <= 1.0 {
				allUnblocked = false
				break
			}
		}
		if !allUnblocked {
			continue
		}

		if wantWays == 3 {
			// Verify it's a 1/X/2 market
			names := map[string]float64{}
			for _, o := range mkt.O {
				names[o.Name] = o.Value
			}
			homeDec, hOK := names["1"]
			drawDec, dOK := names["X"]
			awayDec, aOK := names["2"]
			if !hOK || !dOK || !aOK {
				continue
			}
			h, d, a := removeVig3(homeDec, drawDec, awayDec)
			return &parsedOdds{home: &h, draw: &d, away: &a}
		}

		// 2-way
		names := map[string]float64{}
		for _, o := range mkt.O {
			names[o.Name] = o.Value
		}
		homeDec, hOK := names["1"]
		awayDec, aOK := names["2"]
		if !hOK || !aOK {
			continue
		}
		h, a := removeVig2(homeDec, awayDec)
		return &parsedOdds{home: &h, away: &a}
	}

	return nil
}

func removeVig2(a, b float64) (float64, float64) {
	rawA := 1.0 / a
	rawB := 1.0 / b
	total := rawA + rawB
	return rawA / total, rawB / total
}

func removeVig3(a, b, c float64) (float64, float64, float64) {
	rawA := 1.0 / a
	rawB := 1.0 / b
	rawC := 1.0 / c
	total := rawA + rawB + rawC
	return rawA / total, rawB / total, rawC / total
}

// finishStateCodes are GoalServe state codes that indicate a finished game.
// Populated via log-and-learn; add new codes as they are observed.
var finishStateCodes = map[string]bool{
	"31000": true, // FT (full time)
	"31270": true, // AET (after extra time)
	"31280": true, // AP (after penalties)
	"91000": true, // Cancelled
	"90000": true, // Abandoned
}

func inferMatchStatus(sport events.Sport, msg *UpdtMessage, homeScore, awayScore int) events.MatchStatus {
	if msg.PC == 255 {
		return events.StatusGameFinish
	}

	if msg.SC != "" && finishStateCodes[msg.SC] {
		telemetry.Infof("goalserve_ws: game %s finished via sc=%s (pc=%d)", msg.ID, msg.SC, msg.PC)
		return events.StatusGameFinish
	}

	if homeScore == 0 && awayScore == 0 {
		switch sport {
		case events.SportSoccer:
			if msg.PC == 1 && msg.ET <= 300 {
				return events.StatusGameStart
			}
		case events.SportHockey:
			if msg.PC == 1 && msg.ET <= 180 {
				return events.StatusGameStart
			}
		}
	}

	if sport == events.SportHockey && (msg.PC == 4 || msg.PC == 5) {
		return events.StatusOvertime
	}

	return events.StatusLive
}
