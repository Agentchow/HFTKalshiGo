package fanout

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	clientSendBuf = 256
	writeDeadline = 5 * time.Second
	pongWait      = 30 * time.Second
	pingInterval  = 20 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

type sportClient struct {
	sport events.Sport
	conn  *websocket.Conn
	send  chan []byte
	done  chan struct{}
}

// Server fans out bus events to connected sport WebSocket clients.
type Server struct {
	mu      sync.Mutex
	clients map[*sportClient]struct{}
}

func NewServer(bus *events.Bus) *Server {
	s := &Server{
		clients: make(map[*sportClient]struct{}),
	}
	bus.Subscribe(events.EventScoreChange, s.forward)
	bus.Subscribe(events.EventGameFinish, s.forward)
	bus.Subscribe(events.EventRedCard, s.forward)
	bus.Subscribe(events.EventMarketData, s.forward)
	bus.Subscribe(events.EventWSStatus, s.forward)
	return s
}

// forward is called on the publisher's goroutine. It serializes the event
// and enqueues it to matching clients' send channels (non-blocking).
func (s *Server) forward(evt events.Event) error {
	data, err := MarshalEvent(evt)
	if err != nil {
		telemetry.Warnf("fanout: marshal error: %v", err)
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for c := range s.clients {
		if evt.Type != events.EventMarketData && evt.Type != events.EventWSStatus && c.sport != evt.Sport {
			continue
		}
		select {
		case c.send <- data:
		default:
			telemetry.Warnf("fanout: dropping message for slow client sport=%s", c.sport)
		}
	}
	return nil
}

// HandleWS is the HTTP handler for WebSocket upgrade requests.
// Sport processes connect with ?sport=hockey (etc.)
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	sport := events.Sport(r.URL.Query().Get("sport"))
	if sport == "" {
		http.Error(w, "missing ?sport= query param", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		telemetry.Warnf("fanout: upgrade failed: %v", err)
		return
	}

	c := &sportClient{
		sport: sport,
		conn:  conn,
		send:  make(chan []byte, clientSendBuf),
		done:  make(chan struct{}),
	}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	telemetry.Plainf("Fanout: Client Connected [%s]", strings.ToUpper(string(sport)[:1])+string(sport)[1:])

	go s.writePump(c)
	go s.readPump(c)
}

// writePump drains the client's send channel and writes to the WS connection.
// It owns the client lifecycle: on exit it removes the client from the map
// (so forward never sends to a stale channel) and closes the connection.
func (s *Server) writePump(c *sportClient) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		s.removeClient(c)
		c.conn.Close()
	}()

	for {
		select {
		case msg := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				telemetry.Warnf("fanout: write error sport=%s: %v", c.sport, err)
				return
			}
		case <-c.done:
			return
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump keeps the connection alive by reading pongs / close frames.
// No upstream messages are expected from sport processes.
// On exit it signals writePump via c.done (never closes c.send).
func (s *Server) readPump(c *sportClient) {
	defer close(c.done)

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
	}
}

func (s *Server) removeClient(c *sportClient) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	telemetry.Plainf("Fanout: Client Disconnected [%s]", strings.ToUpper(string(c.sport)[:1])+string(c.sport)[1:])
}

// ListenAndServe starts the fanout WebSocket server.
func (s *Server) ListenAndServe(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.HandleWS)

	addr := fmt.Sprintf(":%d", port)
	telemetry.Plainf("fanout: server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}
