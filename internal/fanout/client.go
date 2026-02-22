package fanout

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	minBackoff = 1 * time.Second
	maxBackoff = 30 * time.Second
)

// Client connects to the central fanout server and republishes
// received events onto a local in-process bus.
type Client struct {
	addr  string
	sport events.Sport
	bus   *events.Bus
}

func NewClient(addr string, sport events.Sport, bus *events.Bus) *Client {
	return &Client{
		addr:  addr,
		sport: sport,
		bus:   bus,
	}
}

// ConnectWithRetry connects to the fanout server and reconnects on failure
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
		backoff := time.Duration(float64(minBackoff) * math.Pow(2, float64(min(attempt-1, 5))))
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		if err != nil {
			telemetry.Warnf("fanout: connection lost (attempt %d): %v — retrying in %s", attempt, err, backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (c *Client) connect(ctx context.Context) error {
	url := fmt.Sprintf("ws://%s/ws?sport=%s", c.addr, c.sport)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	defer conn.Close()

	telemetry.Infof("fanout: connected to %s as sport=%s", c.addr, c.sport)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		evt, err := UnmarshalEvent(msg)
		if err != nil {
			telemetry.Warnf("fanout: unmarshal error: %v", err)
			continue
		}

		c.bus.Publish(evt)
	}
}
