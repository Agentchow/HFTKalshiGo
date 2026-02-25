package goalserve_ws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	tokenMaxAge   = 55 * time.Minute // refresh 5 min before the 60-min expiry
	tokenCooldown = 65 * time.Second // minimum interval between auth requests
)

// TokenProvider caches a GoalServe JWT and rate-limits auth requests so
// multiple WS clients sharing the same API key don't trigger 429s.
// The token is persisted to disk so restarts don't burn an auth request.
type TokenProvider struct {
	mu        sync.Mutex
	authURL   string
	apiKey    string
	token     string
	fetchedAt time.Time
	lastTry   time.Time
	cachePath string
}

type tokenCache struct {
	Token     string    `json:"token"`
	FetchedAt time.Time `json:"fetched_at"`
}

func NewTokenProvider(authURL, apiKey, cachePath string) *TokenProvider {
	tp := &TokenProvider{authURL: authURL, apiKey: apiKey, cachePath: cachePath}
	tp.loadCache()
	return tp
}

func (tp *TokenProvider) loadCache() {
	if tp.cachePath == "" {
		return
	}
	raw, err := os.ReadFile(tp.cachePath)
	if err != nil {
		return
	}
	var cached tokenCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return
	}
	if cached.Token != "" {
		tp.token = cached.Token
		tp.fetchedAt = cached.FetchedAt
		age := time.Since(cached.FetchedAt).Round(time.Second)
		if age > tokenMaxAge {
			telemetry.Infof("goalserve_ws: loaded stale cached token (age %s) — will try before re-auth", age)
		} else {
			telemetry.Infof("goalserve_ws: loaded cached token (age %s)", age)
		}
	}
}

func (tp *TokenProvider) saveCache() {
	if tp.cachePath == "" {
		return
	}
	raw, err := json.Marshal(tokenCache{Token: tp.token, FetchedAt: tp.fetchedAt})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(tp.cachePath), 0o755)
	_ = os.WriteFile(tp.cachePath, raw, 0o600)
}

// Invalidate clears the cached token so the next Token() call fetches a fresh one.
func (tp *TokenProvider) Invalidate() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.token = ""
	tp.fetchedAt = time.Time{}
}

// Token returns a cached JWT if one exists, or fetches a fresh one.
// A stale token is returned as-is so the caller can attempt a connection
// without hitting the auth endpoint; call Invalidate after a rejection.
// When rate-limited with no cached token, blocks until the cooldown expires
// (or ctx is cancelled) instead of returning an error immediately.
func (tp *TokenProvider) Token(ctx context.Context) (string, error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.token != "" {
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
	telemetry.Infof("goalserve_ws: fetching new token from %s", tp.authURL)
	tok, err := fetchToken(tp.authURL, tp.apiKey)
	if err != nil {
		return "", err
	}
	tp.token = tok
	tp.fetchedAt = time.Now()
	tp.saveCache()
	telemetry.Infof("goalserve_ws: new token acquired")
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
