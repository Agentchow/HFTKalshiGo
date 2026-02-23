package events

// ScoreChangeEvent is published when GoalServe reports a score update.
type ScoreChangeEvent struct {
	EID       string  `json:"eid"`
	Sport     Sport   `json:"sport"`
	League    string  `json:"league"`
	HomeTeam  string  `json:"home_team"`
	AwayTeam  string  `json:"away_team"`
	HomeScore int     `json:"home_score"`
	AwayScore int     `json:"away_score"`
	Period    string  `json:"period"` // "1st Period", "2nd Half", "Q3", etc.
	TimeLeft  float64 `json:"time_left"`
	Overturn  bool    `json:"overturn,omitempty"` // true if this score was confirmed after a drop

	// Scheduled kick-off / puck-drop from GoalServe (Unix UTC seconds).
	// Zero when GoalServe doesn't provide it (some hockey feeds).
	GameStartUTC int64 `json:"game_start_utc,omitempty"`

	// Webhook odds (Pinnacle-implied), nil if unavailable.
	HomeWinPct *float64 `json:"home_win_pct,omitempty"`
	DrawPct    *float64 `json:"draw_pct,omitempty"` // soccer only
	AwayWinPct *float64 `json:"away_win_pct,omitempty"`

	// Soccer red card counts from the current webhook snapshot.
	HomeRedCards int `json:"home_red_cards,omitempty"`
	AwayRedCards int `json:"away_red_cards,omitempty"`
}

// MarketEvent is published when the Kalshi WebSocket reports a price change.
// The WS ticker channel sends yes_bid_dollars and yes_ask_dollars (not no_bid/no_ask).
type MarketEvent struct {
	Ticker string  `json:"ticker"`
	YesBid float64 `json:"yes_bid"`
	YesAsk float64 `json:"yes_ask"`
	Volume int64   `json:"volume"`
}

// OrderIntent is published by a strategy when it wants to place an order.
// The execution service subscribes and handles risk checks + placement.
type OrderIntent struct {
	Sport    Sport   `json:"sport"`
	League   string  `json:"league"`
	GameID   string  `json:"game_id"`
	EID      string  `json:"eid"`
	Ticker   string  `json:"ticker"`
	Side     string  `json:"side"`    // "yes" or "no"
	Outcome  string  `json:"outcome"` // "home", "away", "draw"
	LimitPct float64 `json:"limit_pct"`
	Reason   string  `json:"reason"`

	// Context for idempotency: orders are deduped per (ticker, home_score, away_score).
	HomeScore int `json:"home_score"`
	AwayScore int `json:"away_score"`
}

// GameFinishEvent is published when a game reaches final status.
type GameFinishEvent struct {
	EID        string `json:"eid"`
	Sport      Sport  `json:"sport"`
	League     string `json:"league"`
	HomeTeam   string `json:"home_team"`
	AwayTeam   string `json:"away_team"`
	HomeScore  int    `json:"home_score"`
	AwayScore  int    `json:"away_score"`
	FinalState string `json:"final_state"` // "Finished", "After Overtime", etc.
}

// RedCardEvent is soccer-specific.
type RedCardEvent struct {
	EID                   string  `json:"eid"`
	Team                  int     `json:"team"` // 1=home, 2=away
	MinutesRemainingAtRed float64 `json:"minutes_remaining_at_red"`
}

// WSStatusEvent signals Kalshi WebSocket connect/disconnect to sport processes.
type WSStatusEvent struct {
	Connected bool `json:"connected"`
}
