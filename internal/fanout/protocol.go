package fanout

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
)

// Envelope is the wire format for events sent over the fanout WebSocket.
type Envelope struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Sport     events.Sport    `json:"sport,omitempty"`
	League    string          `json:"league,omitempty"`
	GameID    string          `json:"game_id,omitempty"`
	Timestamp time.Time       `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

// MarshalEvent serializes an Event into a JSON-encoded Envelope.
func MarshalEvent(evt events.Event) ([]byte, error) {
	payload, err := json.Marshal(evt.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	env := Envelope{
		Type:      string(evt.Type),
		ID:        evt.ID,
		Sport:     evt.Sport,
		League:    evt.League,
		GameID:    evt.GameID,
		Timestamp: evt.Timestamp,
		Payload:   payload,
	}
	return json.Marshal(env)
}

// UnmarshalEvent deserializes a JSON Envelope back into a typed Event.
func UnmarshalEvent(data []byte) (events.Event, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return events.Event{}, fmt.Errorf("unmarshal envelope: %w", err)
	}

	evt := events.Event{
		ID:        env.ID,
		Type:      events.EventType(env.Type),
		Sport:     env.Sport,
		League:    env.League,
		GameID:    env.GameID,
		Timestamp: env.Timestamp,
	}

	switch evt.Type {
	case events.EventGameUpdate:
		var gu events.GameUpdateEvent
		if err := json.Unmarshal(env.Payload, &gu); err != nil {
			return evt, fmt.Errorf("unmarshal game_update: %w", err)
		}
		evt.Payload = gu
	case events.EventMarketData:
		var me events.MarketEvent
		if err := json.Unmarshal(env.Payload, &me); err != nil {
			return evt, fmt.Errorf("unmarshal market_data: %w", err)
		}
		evt.Payload = me
	case events.EventWSStatus:
		var ws events.WSStatusEvent
		if err := json.Unmarshal(env.Payload, &ws); err != nil {
			return evt, fmt.Errorf("unmarshal ws_status: %w", err)
		}
		evt.Payload = ws
	default:
		return evt, fmt.Errorf("unknown event type: %s", env.Type)
	}

	return evt, nil
}
