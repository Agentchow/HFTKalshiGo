package kalshi_http

import (
	"context"
	"encoding/json"
	"fmt"
)

type BalanceResponse struct {
	Balance int `json:"balance"` // cents
}

func (c *Client) GetBalance(ctx context.Context) (int, error) {
	body, status, err := c.Get(ctx, "/trade-api/v2/portfolio/balance")
	if err != nil {
		return 0, err
	}
	if status != 200 {
		return 0, fmt.Errorf("get balance: status=%d body=%s", status, string(body))
	}
	var resp BalanceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("unmarshal balance: %w", err)
	}
	return resp.Balance, nil
}
