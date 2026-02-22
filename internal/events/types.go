package events

// ScoreChangeEvent is published when GoalServe reports a score update.
type ScoreChangeEvent struct {
	EID       string
	Sport     Sport
	League    string
	HomeTeam  string
	AwayTeam  string
	HomeScore int
	AwayScore int
	Period    string // "1st Period", "2nd Half", "Q3", etc.
	TimeLeft  float64
	Overturn  bool // true if this score was confirmed after a drop

	// Scheduled kick-off / puck-drop from GoalServe (Unix UTC seconds).
	// Zero when GoalServe doesn't provide it (some hockey feeds).
	GameStartUTC int64

	// Webhook odds (Pinnacle-implied), nil if unavailable.
	HomeWinPct *float64
	DrawPct    *float64 // soccer only
	AwayWinPct *float64
}

// MarketEvent is published when the Kalshi WebSocket reports a price change.
type MarketEvent struct {
	Ticker string
	YesAsk float64
	YesBid float64
	NoAsk  float64
	NoBid  float64
	Volume int64
}

// OrderIntent is published by a strategy when it wants to place an order.
// The execution service subscribes and handles risk checks + placement.
type OrderIntent struct {
	Sport    Sport
	League   string
	GameID   string
	EID      string
	Ticker   string
	Side     string // "yes" or "no"
	Outcome  string // "home", "away", "draw"
	LimitPct float64
	Reason   string

	// Context for idempotency: orders are deduped per (ticker, home_score, away_score).
	HomeScore int
	AwayScore int
}

// GameFinishEvent is published when a game reaches final status.
type GameFinishEvent struct {
	EID        string
	Sport      Sport
	League     string
	HomeTeam   string
	AwayTeam   string
	HomeScore  int
	AwayScore  int
	FinalState string // "Finished", "After Overtime", etc.
}

// RedCardEvent is soccer-specific.
type RedCardEvent struct {
	EID                   string
	Team                  int // 1=home, 2=away
	MinutesRemainingAtRed float64
}
