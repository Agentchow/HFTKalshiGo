package kalshi_ws

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Client connects to the Kalshi WebSocket feed and publishes
// MarketEvent updates onto the event bus.
type Client struct {
	url     string
	apiKey  string
	bus     *events.Bus
	conn    *websocket.Conn
	mu      sync.Mutex
	done    chan struct{}

	// Tickers we're subscribed to (set externally before Connect).
	tickers []string
}

func NewClient(wsURL, apiKey string, bus *events.Bus) *Client {
	return &Client{
		url:    wsURL,
		apiKey: apiKey,
		bus:    bus,
		done:   make(chan struct{}),
	}
}

func (c *Client) SetTickers(tickers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tickers = tickers
}

// Connect establishes the WebSocket connection and starts the read loop.
func (c *Client) Connect(ctx context.Context) error {
	header := make(map[string][]string)
	if c.apiKey != "" {
		header["Authorization"] = []string{"Bearer " + c.apiKey}
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, header)
	if err != nil {
		return err
	}
	c.conn = conn

	telemetry.Infof("kalshi_ws: connected to %s", c.url)

	go c.readLoop(ctx)
	return nil
}

func (c *Client) readLoop(ctx context.Context) {
	defer func() {
		c.conn.Close()
		close(c.done)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			telemetry.Warnf("kalshi_ws: read error: %v", err)
			return
		}

		evts := ParseMessage(msg)
		for _, evt := range evts {
			c.bus.Publish(evt)
		}
	}
}

func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Client) Done() <-chan struct{} {
	return c.done
}
