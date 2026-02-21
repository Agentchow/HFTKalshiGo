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
	// GoalServe/Genius Webhook Events
	EventScoreChange EventType = "score_change"
	EventRedCard     EventType = "red_card"
	EventGameFinish  EventType = "game_finish"
	// Kalshi Ticker Events
	EventMarketData EventType = "market_data"
	// Internal Order Events
	EventOrderIntent EventType = "order_intent"
)
