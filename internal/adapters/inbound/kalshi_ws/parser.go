package kalshi_ws

import (
	"encoding/json"
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

type orderBookDelta struct {
	MarketTicker string  `json:"market_ticker"`
	YesAsk       float64 `json:"yes_ask"`
	YesBid       float64 `json:"yes_bid"`
	NoAsk        float64 `json:"no_ask"`
	NoBid        float64 `json:"no_bid"`
	Volume       int64   `json:"volume"`
}

// ParseMessage converts a raw WebSocket frame into domain events.
func ParseMessage(data []byte) []events.Event {
	var msg wsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		telemetry.Warnf("kalshi_ws: parse error: %v", err)
		return nil
	}

	switch msg.Type {
	case "orderbook_snapshot", "orderbook_delta":
		return parseOrderBookUpdate(msg.Msg)
	default:
		return nil
	}
}

func parseOrderBookUpdate(raw json.RawMessage) []events.Event {
	var delta orderBookDelta
	if err := json.Unmarshal(raw, &delta); err != nil {
		return nil
	}
	if delta.MarketTicker == "" {
		return nil
	}

	me := events.MarketEvent{
		Ticker: delta.MarketTicker,
		YesAsk: delta.YesAsk,
		YesBid: delta.YesBid,
		NoAsk:  delta.NoAsk,
		NoBid:  delta.NoBid,
		Volume: delta.Volume,
	}

	return []events.Event{{
		ID:        delta.MarketTicker,
		Type:      events.EventMarketData,
		Timestamp: time.Now(),
		Payload:   me,
	}}
}
