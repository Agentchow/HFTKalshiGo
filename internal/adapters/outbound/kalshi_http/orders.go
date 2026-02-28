package kalshi_http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/charleschow/hft-trading/internal/telemetry"
)

// CreateOrderRequest is the payload for POST /trade-api/v2/portfolio/orders.
type CreateOrderRequest struct {
	Ticker          string `json:"ticker"`
	Action          string `json:"action"`                      // "buy" or "sell"
	Side            string `json:"side"`                        // "yes" or "no"
	Type            string `json:"type"`                        // "limit" or "market"
	CountFP         string `json:"count_fp,omitempty"`          // e.g. "1.00"
	YesPriceDollars string `json:"yes_price_dollars,omitempty"` // e.g. "0.0100"
	NoPriceDollars  string `json:"no_price_dollars,omitempty"`  // e.g. "0.0100"
	ClientID        string `json:"client_order_id,omitempty"`
	TimeInForce     string `json:"time_in_force,omitempty"`     // "good_till_canceled", "immediate_or_cancel", "fill_or_kill"
	ExpirationTS    int64  `json:"expiration_ts,omitempty"`
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
	telemetry.Infof("kalshi: order placed ticker=%s side=%s count=%s -> %s",
		req.Ticker, req.Side, req.CountFP, resp.Order.OrderID)

	return &resp, nil
}

// BatchCreateOrdersRequest is the payload for POST /trade-api/v2/portfolio/orders/batched.
type BatchCreateOrdersRequest struct {
	Orders []CreateOrderRequest `json:"orders"`
}

type BatchCreateOrdersResponse struct {
	Orders []BatchCreateOrdersIndividualResponse `json:"orders"`
}

type BatchCreateOrdersIndividualResponse struct {
	Order *OrderDetail `json:"order"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

type OrderDetail struct {
	OrderID       string `json:"order_id"`
	Status        string `json:"status"`
	Side          string `json:"side"`
	YesPrice      int    `json:"yes_price"`
	NoPrice       int    `json:"no_price"`
	FillCount     int    `json:"fill_count"`
	RemainingCount int   `json:"remaining_count"`
	TakerFees     int    `json:"taker_fees"`
	MakerFees     int    `json:"maker_fees"`
	TakerFillCost int    `json:"taker_fill_cost"`
	MakerFillCost int    `json:"maker_fill_cost"`
}

func (c *Client) PlaceBatchOrders(ctx context.Context, req BatchCreateOrdersRequest) (*BatchCreateOrdersResponse, error) {
	body, status, err := c.Post(ctx, "/trade-api/v2/portfolio/orders/batched", req)
	if err != nil {
		telemetry.Metrics.OrderErrors.Inc()
		return nil, err
	}
	if status < 200 || status >= 300 {
		telemetry.Metrics.OrderErrors.Inc()
		return nil, fmt.Errorf("batch order rejected: status=%d body=%s", status, string(body))
	}

	var resp BatchCreateOrdersResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal batch order response: %w", err)
	}

	for _, r := range resp.Orders {
		if r.Error != nil {
			telemetry.Warnf("kalshi: batch order error: %s", r.Error.Message)
			telemetry.Metrics.OrderErrors.Inc()
		} else if r.Order != nil {
			telemetry.Metrics.OrdersSent.Inc()
		}
	}

	return &resp, nil
}

func (c *Client) GetOrder(ctx context.Context, orderID string) (*OrderDetail, error) {
	path := fmt.Sprintf("/trade-api/v2/portfolio/orders/%s", orderID)
	body, status, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("get order: status=%d body=%s", status, string(body))
	}
	var resp struct {
		Order OrderDetail `json:"order"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal order: %w", err)
	}
	return &resp.Order, nil
}

// ReadTokens returns the current number of available read rate-limit tokens.
func (c *Client) ReadTokens() float64 {
	return c.readLimiter.Tokens()
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
	YesAskDollars          string `json:"yes_ask_dollars"`
	YesBidDollars          string `json:"yes_bid_dollars"`
	NoAskDollars           string `json:"no_ask_dollars"`
	NoBidDollars           string `json:"no_bid_dollars"`
	MutuallyExclusive      bool   `json:"mutually_exclusive"`
}

func (m Market) EffectiveYesAsk() int { return dollarsToCentsInt(m.YesAskDollars) }
func (m Market) EffectiveYesBid() int { return dollarsToCentsInt(m.YesBidDollars) }
func (m Market) EffectiveNoAsk() int  { return dollarsToCentsInt(m.NoAskDollars) }
func (m Market) EffectiveNoBid() int  { return dollarsToCentsInt(m.NoBidDollars) }

func dollarsToCentsInt(s string) int {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(math.Round(v * 100))
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
