package kalshi_http

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/charleschow/hft-trading/internal/telemetry"
)

// CreateOrderRequest is the payload for POST /trade-api/v2/portfolio/orders.
type CreateOrderRequest struct {
	Ticker   string `json:"ticker"`
	Action   string `json:"action"` // "buy" or "sell"
	Side     string `json:"side"`   // "yes" or "no"
	Type     string `json:"type"`   // "limit" or "market"
	Count    int    `json:"count"`
	YesPrice int    `json:"yes_price,omitempty"`
	NoPrice  int    `json:"no_price,omitempty"`
	ClientID string `json:"client_order_id,omitempty"`
}

type CreateOrderResponse struct {
	Order struct {
		OrderID string `json:"order_id"`
		Status  string `json:"status"`
	} `json:"order"`
}

func (c *Client) PlaceOrder(ctx context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	body, status, err := c.Post(ctx, "/trade-api/v2/portfolio/orders", req)
	if err != nil {
		telemetry.Metrics.OrderErrors.Inc()
		return nil, err
	}
	if status < 200 || status >= 300 {
		telemetry.Metrics.OrderErrors.Inc()
		return nil, fmt.Errorf("order rejected: status=%d body=%s", status, string(body))
	}

	var resp CreateOrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal order response: %w", err)
	}

	telemetry.Metrics.OrdersSent.Inc()
	telemetry.Infof("kalshi: order placed ticker=%s side=%s count=%d -> %s",
		req.Ticker, req.Side, req.Count, resp.Order.OrderID)

	return &resp, nil
}

func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	path := fmt.Sprintf("/trade-api/v2/portfolio/orders/%s", orderID)
	_, status, err := c.Delete(ctx, path)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("cancel failed: status=%d", status)
	}
	return nil
}

// Market represents a single Kalshi market from the API.
type Market struct {
	Ticker                 string `json:"ticker"`
	EventTicker            string `json:"event_ticker"`
	Title                  string `json:"title"`
	Subtitle               string `json:"subtitle"`
	YesSubTitle            string `json:"yes_sub_title"`
	NoSubTitle             string `json:"no_sub_title"`
	Status                 string `json:"status"`
	ExpectedExpirationTime string `json:"expected_expiration_time"`
	CloseTime              string `json:"close_time"`
	Volume                 int64  `json:"volume"`
	YesAsk                 int    `json:"yes_ask"`
	YesBid                 int    `json:"yes_bid"`
	NoAsk                  int    `json:"no_ask"`
	NoBid                  int    `json:"no_bid"`
	MutuallyExclusive      bool   `json:"mutually_exclusive"`
}

type GetMarketsResponse struct {
	Markets []Market `json:"markets"`
	Cursor  string   `json:"cursor"`
}

func (c *Client) GetMarkets(ctx context.Context, seriesTicker string) ([]Market, error) {
	var all []Market
	cursor := ""
	for {
		path := fmt.Sprintf("/trade-api/v2/markets?status=open&series_ticker=%s&limit=1000", seriesTicker)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		body, status, err := c.Get(ctx, path)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("get markets: status=%d body=%s", status, string(body))
		}
		var resp GetMarketsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal markets: %w", err)
		}
		all = append(all, resp.Markets...)
		if resp.Cursor == "" || len(resp.Markets) == 0 {
			break
		}
		cursor = resp.Cursor
	}
	return all, nil
}

type PositionResponse struct {
	MarketPositions []struct {
		Ticker         string `json:"ticker"`
		Position       int    `json:"position"`
		MarketExposure int    `json:"market_exposure"`
		RealizedPnl    int    `json:"realized_pnl"`
	} `json:"market_positions"`
}

func (c *Client) GetPositions(ctx context.Context) (*PositionResponse, error) {
	body, status, err := c.Get(ctx, "/trade-api/v2/portfolio/positions")
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("get positions: status=%d", status)
	}
	var resp PositionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
