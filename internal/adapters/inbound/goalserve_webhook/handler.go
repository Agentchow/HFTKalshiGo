package goalserve_webhook

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	parsers map[events.Sport]*Parser
}

func NewHandler(bus *events.Bus) *Handler {
	return &Handler{
		bus: bus,
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

		body, err := h.readBody(r)
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

		telemetry.Infof("goalserve: %s webhook received  raw_events=%d parsed=%d  bytes=%d",
			sport, len(payload.Events), len(evts), len(body))
		for _, evt := range evts {
			if sc, ok := evt.Payload.(events.ScoreChangeEvent); ok {
				telemetry.Infof("  [%s] eid=%s  %s vs %s  %d-%d  %s",
					sc.Sport, sc.EID, sc.AwayTeam, sc.HomeTeam, sc.AwayScore, sc.HomeScore, sc.Period)
			}
			if gf, ok := evt.Payload.(events.GameFinishEvent); ok {
				telemetry.Infof("  [%s] eid=%s  %s vs %s  %d-%d  FINAL (%s)",
					gf.Sport, gf.EID, gf.AwayTeam, gf.HomeTeam, gf.AwayScore, gf.HomeScore, gf.FinalState)
			}
			h.bus.Publish(evt)
		}

		telemetry.Metrics.WebhookLatency.Record(time.Since(start))
	}
}

// readBody handles gzip-compressed and plain payloads.
// GoalServe sends gzip when Content-Encoding is set, but also sometimes
// sends raw gzip bytes without the header — detected by magic bytes.
func (h *Handler) readBody(r *http.Request) ([]byte, error) {
	var reader io.Reader = r.Body
	defer r.Body.Close()

	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip header: %w", err)
		}
		defer gz.Close()
		reader = gz
	} else {
		// Peek first two bytes for gzip magic (0x1f 0x8b)
		buf := make([]byte, 2)
		n, err := io.ReadFull(r.Body, buf)
		if err != nil && n == 0 {
			return nil, fmt.Errorf("empty body")
		}
		if n >= 2 && buf[0] == 0x1f && buf[1] == 0x8b {
			combined := io.MultiReader(
				strings.NewReader(string(buf[:n])),
				r.Body,
			)
			gz, err := gzip.NewReader(combined)
			if err != nil {
				return nil, fmt.Errorf("gzip magic: %w", err)
			}
			defer gz.Close()
			reader = gz
		} else {
			reader = io.MultiReader(
				strings.NewReader(string(buf[:n])),
				r.Body,
			)
		}
	}

	return io.ReadAll(reader)
}

func (h *Handler) healthCheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok","adapter":"goalserve_webhook"}`))
}
