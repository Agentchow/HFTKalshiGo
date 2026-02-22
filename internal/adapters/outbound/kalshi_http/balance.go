package kalshi_http

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"
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
		return 0, fmt.Errorf("get balance: status=%d", status)
	}
	var resp BalanceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("unmarshal balance: %w", err)
	}
	return resp.Balance, nil
}

// BalanceCache wraps GetBalance with a TTL-based cache and optional
// background refresh to avoid redundant API calls during rapid order sizing.
type BalanceCache struct {
	client    *Client
	ttl       time.Duration
	refreshAt time.Duration

	mu        sync.RWMutex
	cached    int
	fetchedAt time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewBalanceCache creates a cache with the given TTL. If bgInterval > 0,
// a background goroutine refreshes the cache at that interval.
func NewBalanceCache(client *Client, ttl, bgInterval time.Duration) *BalanceCache {
	bc := &BalanceCache{
		client:    client,
		ttl:       ttl,
		refreshAt: bgInterval,
		stopCh:    make(chan struct{}),
	}

	if bgInterval > 0 {
		go bc.backgroundRefresh(bgInterval)
	}

	return bc
}

// Get returns the cached balance, refreshing if stale.
func (bc *BalanceCache) Get(ctx context.Context) (int, error) {
	bc.mu.RLock()
	if time.Since(bc.fetchedAt) < bc.ttl && bc.fetchedAt.After(time.Time{}) {
		val := bc.cached
		bc.mu.RUnlock()
		return val, nil
	}
	bc.mu.RUnlock()

	return bc.refresh(ctx)
}

// Invalidate forces the next Get to fetch fresh data.
func (bc *BalanceCache) Invalidate() {
	bc.mu.Lock()
	bc.fetchedAt = time.Time{}
	bc.mu.Unlock()
}

func (bc *BalanceCache) refresh(ctx context.Context) (int, error) {
	bal, err := bc.client.GetBalance(ctx)
	if err != nil {
		return 0, err
	}

	bc.mu.Lock()
	bc.cached = bal
	bc.fetchedAt = time.Now()
	bc.mu.Unlock()

	telemetry.Debugf("balance_cache: refreshed balance=%d cents", bal)
	return bal, nil
}

func (bc *BalanceCache) backgroundRefresh(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if _, err := bc.refresh(ctx); err != nil {
				telemetry.Warnf("balance_cache: background refresh failed: %v", err)
			}
			cancel()
		case <-bc.stopCh:
			return
		}
	}
}

func (bc *BalanceCache) Stop() {
	bc.stopOnce.Do(func() { close(bc.stopCh) })
}
