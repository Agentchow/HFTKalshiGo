package kalshi_ws

import (
	"context"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Client connects to the Kalshi WebSocket feed and publishes
// MarketEvent updates onto the event bus.
type Client struct {
	url    string
	signer *kalshi_auth.Signer
	bus    *events.Bus
	conn   *websocket.Conn
	done   chan struct{}

	// Tickers to subscribe to on connect (set via SetTickers before Connect).
	tickers []string
}

func NewClient(wsURL string, signer *kalshi_auth.Signer, bus *events.Bus) *Client {
	return &Client{
		url:    wsURL,
		signer: signer,
		bus:    bus,
		done:   make(chan struct{}),
	}
}

func (c *Client) SetTickers(tickers []string) {
	c.tickers = tickers
}

func (c *Client) Connect(ctx context.Context) error {
	parsed, _ := url.Parse(c.url)
	wsPath := parsed.Path
	if wsPath == "" {
		wsPath = "/trade-api/ws/v2"
	}
	header := c.signer.Headers("GET", wsPath)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, header)
	if err != nil {
		return err
	}
	c.conn = conn

	telemetry.Infof("Kalshi WS connected to %s", c.url)

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
			telemetry.Warnf("Kalshi WS read error: %v", err)
			return
		}

		for _, evt := range ParseMessage(msg) {
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
