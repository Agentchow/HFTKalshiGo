package goalserve_ws

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	minBackoff  = 1 * time.Second
	maxBackoff  = 30 * time.Second
	readTimeout = 90 * time.Second
)

// Client connects to the GoalServe Inplay WebSocket for a single sport,
// parses incoming messages, and publishes GameUpdateEvents to the bus.
type Client struct {
	sport         string // GoalServe sport key: "soccer", "hockey", "amfootball"
	wsBaseURL     string // e.g. "ws://LIVE.goalserve.com/ws"
	tokenProvider *TokenProvider
	bus           *events.Bus
	store         *Store

	seenGames map[string]bool // track first parse per game for debug logging
}

func NewClient(sport, wsBaseURL string, tp *TokenProvider, bus *events.Bus, store *Store) *Client {
	return &Client{
		sport:         sport,
		wsBaseURL:     wsBaseURL,
		tokenProvider: tp,
		bus:           bus,
		store:         store,
		seenGames:     make(map[string]bool),
	}
}

// ConnectWithRetry connects to GoalServe WS and reconnects on failure
// with exponential backoff. Blocks until ctx is cancelled.
func (c *Client) ConnectWithRetry(ctx context.Context) {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}

		connStart := time.Now()
		err := c.connect(ctx)
		if ctx.Err() != nil {
			return
		}

		if time.Since(connStart) > time.Minute {
			attempt = 0
		}

		attempt++
		telemetry.Metrics.WSReconnects.Inc()
		backoff := time.Duration(float64(minBackoff) * math.Pow(2, float64(min(attempt-1, 5))))
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		if err != nil && strings.Contains(err.Error(), "auth:") {
			backoff = tokenCooldown
		}

		if err != nil {
			telemetry.Warnf("goalserve_ws[%s]: connection lost (attempt %d): %v — retrying in %s",
				c.sport, attempt, err, backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// isAuthRejection returns true if the error indicates the server rejected the
// connection due to auth (401/403, bad handshake). Network errors (connection
// reset, timeout, refused) return false so we retry with the cached token.
func isAuthRejection(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "bad handshake") ||
		strings.Contains(s, "401") ||
		strings.Contains(s, "403") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "forbidden")
}

func (c *Client) connect(ctx context.Context) error {
	token, err := c.tokenProvider.Token(ctx)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	url := fmt.Sprintf("%s/%s?tkn=%s", c.wsBaseURL, c.sport, token)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		// Only invalidate on auth rejection (401/403, bad handshake). Network
		// errors (connection reset, timeout, refused) should retry with cached token.
		if isAuthRejection(err) {
			c.tokenProvider.Invalidate()
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Reset deadline on server pings so quiet periods don't trigger a timeout.
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	telemetry.Infof("goalserve_ws[%s]: connected", c.sport)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		conn.SetReadDeadline(time.Now().Add(readTimeout))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		telemetry.Metrics.WSMessagesReceived.Inc()

		var envelope WSMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			telemetry.Metrics.WSParseErrors.Inc()
			telemetry.Warnf("goalserve_ws[%s]: unmarshal envelope: %v", c.sport, err)
			continue
		}

		// Persist every raw message before parsing.
		c.store.Insert(c.sport, envelope.MT, raw)

		switch envelope.MT {
		case "updt":
			c.handleUpdt(raw)
		case "avl":
			c.handleAvl(raw)
		default:
			telemetry.Debugf("goalserve_ws[%s]: unknown message type %q", c.sport, envelope.MT)
		}
	}
}

func (c *Client) handleUpdt(raw []byte) {
	var msg UpdtMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		telemetry.Metrics.WSParseErrors.Inc()
		telemetry.Warnf("goalserve_ws[%s]: parse updt: %v", c.sport, err)
		return
	}

	// Measure latency from GoalServe push time to local processing.
	if msg.PT != "" {
		if ptMillis, err := strconv.ParseInt(msg.PT, 10, 64); err == nil {
			latency := time.Since(time.UnixMilli(ptMillis))
			telemetry.Metrics.WSLatency.Record(latency)
		}
	}

	evt := ParseUpdt(&msg)
	if evt == nil {
		return
	}

	if !c.seenGames[msg.ID] {
		c.seenGames[msg.ID] = true
		telemetry.Debugf("goalserve_ws[%s]: new game id=%s  %q vs %q  league=%q  pc=%d",
			c.sport, msg.ID, msg.T1.Name, msg.T2.Name, msg.CmpName, msg.PC)
	}

	telemetry.Metrics.EventsProcessed.Inc()
	c.bus.Publish(*evt)
}

func (c *Client) handleAvl(raw []byte) {
	var msg AvlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		telemetry.Debugf("goalserve_ws[%s]: parse avl: %v", c.sport, err)
		return
	}
	telemetry.Debugf("goalserve_ws[%s]: avl — %d LIVE events", c.sport, len(msg.Evts))
}
