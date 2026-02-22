package execution

import (
	"context"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
)

// OrderPlacer abstracts the ability to place orders on an exchange.
// Satisfied by *kalshi_http.Client.
type OrderPlacer interface {
	PlaceOrder(ctx context.Context, req kalshi_http.CreateOrderRequest) (*kalshi_http.CreateOrderResponse, error)
}
