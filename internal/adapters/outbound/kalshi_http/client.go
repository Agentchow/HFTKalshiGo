package kalshi_http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/telemetry"
	"golang.org/x/time/rate"
)

type Client struct {
	baseURL      string
	httpClient   *http.Client
	signer       *kalshi_auth.Signer
	readLimiter  *rate.Limiter
	writeLimiter *rate.Limiter
}

func NewClient(baseURL string, signer *kalshi_auth.Signer, rateDivisor int) *Client {
	if rateDivisor < 1 {
		rateDivisor = 1
	}
	readRate := rate.Limit(20 / float64(rateDivisor))
	writeRate := rate.Limit(10 / float64(rateDivisor))
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		signer:       signer,
		readLimiter:  rate.NewLimiter(readRate, max(1, 20/rateDivisor)),
		writeLimiter: rate.NewLimiter(writeRate, max(1, 10/rateDivisor)),
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	lim := c.readLimiter
	if method != http.MethodGet {
		lim = c.writeLimiter
	}
	rlStart := time.Now()
	if err := lim.Wait(ctx); err != nil {
		return nil, 0, fmt.Errorf("rate limit wait: %w", err)
	}
	if rlWait := time.Since(rlStart); rlWait > time.Millisecond {
		telemetry.Metrics.RateLimiterWait.Record(rlWait)
		telemetry.Debugf("kalshi_http: rate limiter wait %s for %s %s", rlWait, method, path)
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if err := c.signer.SignRequest(req); err != nil {
		return nil, 0, fmt.Errorf("sign: %w", err)
	}

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	telemetry.Debugf("kalshi_http: %s %s -> %d (%s)", method, path, resp.StatusCode, time.Since(start))

	return respBody, resp.StatusCode, nil
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, int, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *Client) Post(ctx context.Context, path string, body any) ([]byte, int, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

func (c *Client) Delete(ctx context.Context, path string) ([]byte, int, error) {
	return c.do(ctx, http.MethodDelete, path, nil)
}
