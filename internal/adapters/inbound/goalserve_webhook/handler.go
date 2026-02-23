package goalserve_webhook

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Handler receives GoalServe webhook POSTs, decompresses gzip payloads,
// parses per-sport events, and publishes them onto the event bus.
//
// Routes:
//   POST /webhook/hockey  -> sport=hockey
//   POST /webhook/soccer  -> sport=soccer
//   POST /webhook/football -> sport=football
//   GET  /health          -> 200 OK
type Handler struct {
	bus     *events.Bus
	store   *Store
	parsers map[events.Sport]*Parser
}

func NewHandler(bus *events.Bus, store *Store) *Handler {
	return &Handler{
		bus:   bus,
		store: store,
		parsers: map[events.Sport]*Parser{
			events.SportHockey:   NewParser(events.SportHockey),
			events.SportSoccer:   NewParser(events.SportSoccer),
			events.SportFootball: NewParser(events.SportFootball),
		},
	}
}

// RegisterRoutes wires HTTP routes onto the provided mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhook/hockey", h.handle(events.SportHockey))
	mux.HandleFunc("POST /webhook/soccer", h.handle(events.SportSoccer))
	mux.HandleFunc("POST /webhook/football", h.handle(events.SportFootball))
	// Legacy: bare POST goes to hockey (matches Python start.py behavior)
	mux.HandleFunc("POST /webhook", h.handle(events.SportHockey))
	mux.HandleFunc("POST /webhook/goalserve", h.handle(events.SportHockey))
	mux.HandleFunc("GET /health", h.healthCheck)
}

func (h *Handler) handle(sport events.Sport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		telemetry.Metrics.WebhooksReceived.Inc()

		raw, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil || len(raw) == 0 {
			telemetry.Metrics.WebhookParseErrors.Inc()
			w.WriteHeader(http.StatusOK)
			return
		}

		if h.store != nil {
			h.store.Insert(sport, raw)
		}

		body, err := decompress(raw)
		if err != nil {
			telemetry.Metrics.WebhookParseErrors.Inc()
			w.WriteHeader(http.StatusOK)
			return
		}

		// Respond immediately — GoalServe doesn't use the response.
		w.WriteHeader(http.StatusOK)

		var payload WebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			telemetry.Metrics.WebhookParseErrors.Inc()
			telemetry.Warnf("goalserve: JSON parse error for %s: %v", sport, err)
			return
		}

		parser := h.parsers[sport]
		evts := parser.Parse(&payload)

		telemetry.Debugf("goalserve: %s webhook received  raw_events=%d parsed=%d  bytes=%d",
			sport, len(payload.Events), len(evts), len(body))
		for _, evt := range evts {
			if gu, ok := evt.Payload.(events.GameUpdateEvent); ok {
				telemetry.Debugf("  [%s] eid=%s  %s vs %s  %d-%d  %s  status=%s",
					gu.Sport, gu.EID, gu.AwayTeam, gu.HomeTeam, gu.AwayScore, gu.HomeScore, gu.Period, gu.MatchStatus)
			}
			h.bus.Publish(evt)
		}

		telemetry.Metrics.WebhookLatency.Record(time.Since(start))
	}
}

// decompress inflates gzip payloads (detected by magic bytes) for JSON parsing.
// Raw bytes are already persisted compressed by the Store before this is called.
func decompress(raw []byte) ([]byte, error) {
	if len(raw) < 2 {
		return raw, nil
	}
	if raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		return io.ReadAll(gz)
	}
	return raw, nil
}

func (h *Handler) healthCheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok","adapter":"goalserve_webhook"}`))
}
