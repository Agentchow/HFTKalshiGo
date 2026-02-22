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

type tickerMsg struct {
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
		Ticker: t.MarketTicker,
		YesAsk: t.YesAsk,
		YesBid: t.YesBid,
		NoAsk:  t.NoAsk,
		NoBid:  t.NoBid,
		Volume: t.Volume,
	}

	return []events.Event{{
		ID:        t.MarketTicker,
		Type:      events.EventMarketData,
		Timestamp: time.Now(),
		Payload:   me,
	}}
}
