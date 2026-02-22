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
	MarketTicker  string  `json:"market_ticker"`
	YesAsk        float64 `json:"yes_ask"`
	YesBid        float64 `json:"yes_bid"`
	NoAsk         float64 `json:"no_ask"`
	NoBid         float64 `json:"no_bid"`
	YesAskDollars string  `json:"yes_ask_dollars"`
	YesBidDollars string  `json:"yes_bid_dollars"`
	NoAskDollars  string  `json:"no_ask_dollars"`
	NoBidDollars  string  `json:"no_bid_dollars"`
	Volume        int64   `json:"volume"`
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

	yesAsk := t.YesAsk
	if yesAsk == 0 && t.YesAskDollars != "" {
		yesAsk = dollarsToCents(t.YesAskDollars)
	}
	yesBid := t.YesBid
	if yesBid == 0 && t.YesBidDollars != "" {
		yesBid = dollarsToCents(t.YesBidDollars)
	}
	noAsk := t.NoAsk
	if noAsk == 0 && t.NoAskDollars != "" {
		noAsk = dollarsToCents(t.NoAskDollars)
	}
	noBid := t.NoBid
	if noBid == 0 && t.NoBidDollars != "" {
		noBid = dollarsToCents(t.NoBidDollars)
	}

	me := events.MarketEvent{
		Ticker: t.MarketTicker,
		YesAsk: yesAsk,
		YesBid: yesBid,
		NoAsk:  noAsk,
		NoBid:  noBid,
		Volume: t.Volume,
	}

	return []events.Event{{
		ID:        t.MarketTicker,
		Type:      events.EventMarketData,
		Timestamp: time.Now(),
		Payload:   me,
	}}
}

func dollarsToCents(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 100
}
