package genius_ws

import (
	"encoding/json"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

type geniusMessage struct {
	Type    string `json:"type"`
	FixtureID string `json:"fixture_id"`
	Sport   string `json:"sport"`
	League  string `json:"league"`
	Home    struct {
		Name  string `json:"name"`
		Score int    `json:"score"`
	} `json:"home"`
	Away struct {
		Name  string `json:"name"`
		Score int    `json:"score"`
	} `json:"away"`
	Period    string  `json:"period"`
	Clock     string  `json:"clock"`
	TimeLeft  float64 `json:"time_left"`
}

func ParseMessage(data []byte) []events.Event {
	var msg geniusMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		telemetry.Warnf("genius_ws: parse error: %v", err)
		return nil
	}

	if msg.FixtureID == "" {
		return nil
	}

	sport := mapSport(msg.Sport)
	gu := events.GameUpdateEvent{
		EID:         msg.FixtureID,
		Source:      "genius_ws",
		Sport:       sport,
		League:      msg.League,
		HomeTeam:    msg.Home.Name,
		AwayTeam:    msg.Away.Name,
		HomeScore:   msg.Home.Score,
		AwayScore:   msg.Away.Score,
		Period:      msg.Period,
		TimeLeft:    msg.TimeLeft,
		MatchStatus: "Live",
	}

	return []events.Event{{
		ID:        msg.FixtureID,
		Type:      events.EventGameUpdate,
		Sport:     sport,
		League:    msg.League,
		GameID:    msg.FixtureID,
		Timestamp: time.Now(),
		Payload:   gu,
	}}
}

func mapSport(s string) events.Sport {
	switch s {
	case "hockey", "ice_hockey":
		return events.SportHockey
	case "soccer", "football":
		return events.SportSoccer
	case "american_football":
		return events.SportFootball
	default:
		return events.Sport(s)
	}
}
