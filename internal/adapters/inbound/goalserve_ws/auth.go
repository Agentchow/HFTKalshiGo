package goalserve_ws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	tokenMaxAge   = 55 * time.Minute  // refresh 5 min before the 60-min expiry
	tokenCooldown = 65 * time.Second  // minimum interval between auth requests
)

// TokenProvider caches a GoalServe JWT and rate-limits auth requests so
// multiple WS clients sharing the same API key don't trigger 429s.
type TokenProvider struct {
	mu        sync.Mutex
	authURL   string
	apiKey    string
	token     string
	fetchedAt time.Time
	lastTry   time.Time
}

func NewTokenProvider(authURL, apiKey string) *TokenProvider {
	return &TokenProvider{authURL: authURL, apiKey: apiKey}
}

// Token returns a cached JWT if still valid, or fetches a fresh one.
// When rate-limited with no cached token, blocks until the cooldown expires
// (or ctx is cancelled) instead of returning an error immediately.
// Only one goroutine fetches per cooldown cycle; others wait and get the cached result.
func (tp *TokenProvider) Token(ctx context.Context) (string, error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.token != "" && time.Since(tp.fetchedAt) < tokenMaxAge {
		return tp.token, nil
	}

	for {
		wait := tokenCooldown - time.Since(tp.lastTry)
		if wait <= 0 {
			break
		}
		if tp.token != "" {
			return tp.token, nil
		}
		telemetry.Infof("goalserve_ws: auth cooldown — waiting %s", wait.Round(time.Second))
		tp.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			tp.mu.Lock()
			return "", ctx.Err()
		}
		tp.mu.Lock()
		if tp.token != "" && time.Since(tp.fetchedAt) < tokenMaxAge {
			return tp.token, nil
		}
	}

	tp.lastTry = time.Now()
	tok, err := fetchToken(tp.authURL, tp.apiKey)
	if err != nil {
		return "", err
	}
	tp.token = tok
	tp.fetchedAt = time.Now()
	return tok, nil
}

// fetchToken exchanges a GoalServe API key for a JWT access token.
// The token is valid for 60 minutes.
func fetchToken(authURL, apiKey string) (string, error) {
	body, err := json.Marshal(map[string]string{"apiKey": apiKey})
	if err != nil {
		return "", fmt.Errorf("marshal auth body: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(authURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("auth request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("auth status %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode auth response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("empty token in auth response")
	}
	return result.Token, nil
}
