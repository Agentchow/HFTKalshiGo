package kalshi_ws

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// wsMessage represents a raw message from the Kalshi WebSocket.
type wsMessage struct {
	Type string          `json:"type"`
	Msg  json.RawMessage `json:"msg"`
	SID  int64           `json:"sid"`
}

type tickerMsg struct {
	MarketTicker  string `json:"market_ticker"`
	YesBidDollars string `json:"yes_bid_dollars"`
	NoBidDollars  string `json:"no_bid_dollars"`
	Volume        int64  `json:"volume"`
}

// ParseMessage converts a raw WebSocket frame into domain events.
func ParseMessage(data []byte) []events.Event {
	var msg wsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		telemetry.Warnf("kalshi_ws: parse error: %v", err)
		return nil
	}

	switch msg.Type {
	case "ticker":
		return parseTickerUpdate(msg.Msg)
	case "subscribed", "unsubscribed", "ok", "error":
		if msg.Type == "error" {
			telemetry.Warnf("kalshi_ws: server error: %s", string(msg.Msg))
		}
		return nil
	default:
		return nil
	}
}

func parseTickerUpdate(raw json.RawMessage) []events.Event {
	var t tickerMsg
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil
	}
	if t.MarketTicker == "" {
		return nil
	}

	me := events.MarketEvent{
		Ticker:    t.MarketTicker,
		YesBid:   dollarsToCents(t.YesBidDollars),
		NoBid:    dollarsToCents(t.NoBidDollars),
		Volume:   t.Volume,
	}

	return []events.Event{{
		ID:        t.MarketTicker,
		Type:      events.EventMarketData,
		Timestamp: time.Now(),
		Payload:   me,
	}}
}

// dollarsToCents converts a dollar-string from the Kalshi WS to cents.
// Returns -1 when the field is absent or unparseable (partial WS update),
// so callers can distinguish "not sent" from "genuinely $0.00".
func dollarsToCents(s string) float64 {
	if s == "" {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v * 100
}
