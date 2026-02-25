package kalshi_ws

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Client connects to the Kalshi WebSocket feed and publishes
// MarketEvent updates onto the event bus.
//
// Gorilla/websocket supports one concurrent reader and one concurrent
// writer, so all writes are serialized through mu.
type Client struct {
	url    string
	signer *kalshi_auth.Signer
	bus    *events.Bus
	conn   *websocket.Conn
	done   chan struct{}

	mu      sync.Mutex
	tickers map[string]bool
	subID   int
}

func NewClient(wsURL string, signer *kalshi_auth.Signer, bus *events.Bus) *Client {
	return &Client{
		url:     wsURL,
		signer:  signer,
		bus:     bus,
		done:    make(chan struct{}),
		tickers: make(map[string]bool),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	if err := c.dial(ctx); err != nil {
		return err
	}
	go c.runLoop(ctx)
	return nil
}

func (c *Client) dial(ctx context.Context) error {
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

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

// SubscribeTickers adds tickers and subscribes on the LIVE connection.
// Safe to call from any goroutine at any time. If the connection is not
// yet established the tickers are stored and subscribed on connect.
func (c *Client) SubscribeTickers(tickers []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var newTickers []string
	for _, t := range tickers {
		if !c.tickers[t] {
			c.tickers[t] = true
			newTickers = append(newTickers, t)
		}
	}

	if len(newTickers) == 0 || c.conn == nil {
		return nil
	}

	return c.sendSubscribe(newTickers)
}

// runLoop reads messages and reconnects on failure with exponential backoff.
func (c *Client) runLoop(ctx context.Context) {
	defer close(c.done)

	first := true
	for {
		if first {
			telemetry.Infof("[Kalshi] WS connected to %s", c.url)
			first = false
		} else {
			telemetry.Infof("Kalshi WS reconnected")
		}

		c.resubscribeAll()
		c.publishWSStatus(true)
		c.readLoop(ctx)
		c.publishWSStatus(false)

		select {
		case <-ctx.Done():
			return
		default:
		}

		backoff := 1 * time.Second
		const maxBackoff = 30 * time.Second
		for attempt := 1; ; attempt++ {
			telemetry.Warnf("Kalshi WS reconnecting (attempt %d) in %s", attempt, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if err := c.dial(ctx); err != nil {
				telemetry.Warnf("Kalshi WS dial failed: %v", err)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			break
		}
	}
}

// resubscribeAll sends a subscribe for every known ticker.
// Called after each successful connection/reconnection.
func (c *Client) resubscribeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.tickers) == 0 {
		return
	}

	all := make([]string, 0, len(c.tickers))
	for t := range c.tickers {
		all = append(all, t)
	}

	if err := c.sendSubscribe(all); err != nil {
		telemetry.Warnf("Kalshi WS resubscribe failed: %v", err)
	}
}

// sendSubscribe writes a subscribe command. Caller must hold mu.
func (c *Client) sendSubscribe(tickers []string) error {
	c.subID++
	cmd := subscribeCmd{
		ID:  c.subID,
		Cmd: "subscribe",
		Params: subscribeParams{
			Channels:            []string{"ticker"},
			MarketTickers:       tickers,
			SendInitialSnapshot: true,
		},
	}
	telemetry.Debugf("kalshi_ws: subscribing to %d tickers (sid=%d)", len(tickers), c.subID)
	return c.conn.WriteJSON(cmd)
}

type subscribeCmd struct {
	ID     int             `json:"id"`
	Cmd    string          `json:"cmd"`
	Params subscribeParams `json:"params"`
}

type subscribeParams struct {
	Channels            []string `json:"channels"`
	MarketTickers       []string `json:"market_tickers,omitempty"`
	SendInitialSnapshot bool     `json:"send_initial_snapshot,omitempty"`
}

func (c *Client) readLoop(ctx context.Context) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	defer conn.Close()

	// Kalshi sends pings every 10s; 30s gives 3 missed pings before timeout.
	const pingWait = 30 * time.Second

	conn.SetReadDeadline(time.Now().Add(pingWait))
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(pingWait))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			telemetry.Warnf("Kalshi WS read error: %v", err)
			return
		}

		conn.SetReadDeadline(time.Now().Add(pingWait))
		for _, evt := range ParseMessage(msg) {
			c.bus.Publish(evt)
		}
	}
}

func (c *Client) publishWSStatus(connected bool) {
	c.bus.Publish(events.Event{
		Type:      events.EventWSStatus,
		Timestamp: time.Now(),
		Payload:   events.WSStatusEvent{Connected: connected},
	})
}

func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (c *Client) Done() <-chan struct{} {
	return c.done
}
