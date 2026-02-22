package ticker

import (
	"context"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
)

// MarketFetcher abstracts fetching open markets from an exchange.
// Satisfied by *kalshi_http.Client.
type MarketFetcher interface {
	GetMarkets(ctx context.Context, seriesTicker string) ([]kalshi_http.Market, error)
}
