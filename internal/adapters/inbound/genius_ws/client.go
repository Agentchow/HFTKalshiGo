package genius_ws

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Client connects to the Genius Sports WebSocket feed for real-time
// score data (lower latency alternative to GoalServe for some sports).
type Client struct {
	url   string
	token string
	bus   *events.Bus
	conn  *websocket.Conn
	mu    sync.Mutex
	done  chan struct{}
}

func NewClient(wsURL, token string, bus *events.Bus) *Client {
	return &Client{
		url:   wsURL,
		token: token,
		bus:   bus,
		done:  make(chan struct{}),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	header := make(map[string][]string)
	if c.token != "" {
		header["Authorization"] = []string{"Bearer " + c.token}
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, header)
	if err != nil {
		return err
	}
	c.conn = conn

	telemetry.Infof("genius_ws: connected to %s", c.url)

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
			telemetry.Warnf("genius_ws: read error: %v", err)
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
