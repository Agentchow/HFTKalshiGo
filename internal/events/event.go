package events

import "time"

type Sport string

const (
	SportHockey   Sport = "hockey"
	SportSoccer   Sport = "soccer"
	SportFootball Sport = "football"
)

// Event is the envelope that flows through the event bus.
// Every domain event (score change, market update, order intent) is wrapped in one.
type Event struct {
	ID        string
	Type      EventType
	Sport     Sport
	League    string
	GameID    string
	Timestamp time.Time
	Payload   any
}

type EventType string

const (
	// GoalServe Webhook/WS and Genius Sports WS events
	EventGameUpdate EventType = "game_update"
	// Kalshi Ticker Events
	EventMarketData EventType = "market_data"
	// Kalshi WebSocket status
	EventWSStatus EventType = "ws_status"
	// Internal Order Events — payload is []OrderIntent (batch)
	EventOrderIntent EventType = "order_intent"
)
