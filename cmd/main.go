package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/inbound/goalserve_webhook"
	"github.com/charleschow/hft-trading/internal/adapters/inbound/kalshi_ws"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/execution"
	"github.com/charleschow/hft-trading/internal/core/execution/lanes"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	footballStrat "github.com/charleschow/hft-trading/internal/core/strategy/football"
	hockeyStrat "github.com/charleschow/hft-trading/internal/core/strategy/hockey"
	soccerStrat "github.com/charleschow/hft-trading/internal/core/strategy/soccer"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

func main() {
	cfg := config.Load()
	initLogging(cfg.LogLevel)

	telemetry.Infof("starting hft-trading system")

	// ── Core infrastructure ──────────────────────────────────────
	// Single event bus that all components publish/subscribe through.
	// When someone calls bus.Publish(event), the bus looks up the event's type in the map, 
	// calls the matching handlers, and the event is gone. 
	// Nothing is saved. There's no queue, no history, no replay.
	bus := events.NewBus()
	// In-memory store of all active games, keyed by (sport, game_id).
	gameStore := store.New()

	// ── Strategy registry ────────────────────────────────────────
	// Maps each sport to its trading strategy so the engine can look up
	// the correct one when a score-change event arrives.
	registry := strategy.NewRegistry()
	registry.Register(events.SportHockey, hockeyStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	registry.Register(events.SportSoccer, soccerStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	registry.Register(events.SportFootball, footballStrat.NewStrategy(cfg.ScoreDropConfirmSec))

	// ── Strategy engine (subscribes to score changes) ────────────
	_ = strategy.NewEngine(bus, gameStore, registry)

	// ── Risk limits from YAML ────────────────────────────────────
	riskLimits, err := config.LoadRiskLimits(cfg.RiskLimitsPath)
	if err != nil {
		telemetry.Warnf("risk_limits: failed to load %s: %v (using defaults)", cfg.RiskLimitsPath, err)
		riskLimits = config.RiskLimits{}
	}

	// ── Execution lane router ────────────────────────────────────
	// Each sport gets a shared SpendGuard for the sport-level cap.
	// Each league under that sport gets its own lane with a per-game cap,
	// but they all share the same sport-level spend tracker.
	laneRouter := execution.NewLaneRouter()
	registerSportLanes(laneRouter, riskLimits, events.SportHockey, "hockey")
	registerSportLanes(laneRouter, riskLimits, events.SportSoccer, "soccer")
	registerSportLanes(laneRouter, riskLimits, events.SportFootball, "football")

	// ── Outbound: Kalshi HTTP client ─────────────────────────────
	kalshiClient := kalshi_http.NewClient(cfg.KalshiBaseURL, cfg.KalshiAPIKey, cfg.KalshiSecret)

	// ── Execution service (subscribes to order intents) ──────────
	_ = execution.NewService(bus, laneRouter, kalshiClient, gameStore)

	// ── Inbound: GoalServe webhook handler ───────────────────────
	webhookHandler := goalserve_webhook.NewHandler(bus)
	mux := http.NewServeMux()
	webhookHandler.RegisterRoutes(mux)

	addr := fmt.Sprintf("%s:%d", cfg.WebhookHost, cfg.WebhookPort)
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ── Start HTTP server ────────────────────────────────────────
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			telemetry.Errorf("http server error: %v", err)
			os.Exit(1)
		}
	}()

	telemetry.Infof("webhook server listening on %s", addr)
	telemetry.Infof("  POST /webhook/hockey")
	telemetry.Infof("  POST /webhook/soccer")
	telemetry.Infof("  POST /webhook/football")
	telemetry.Infof("  GET  /health")

	// ── ngrok tunnel ─────────────────────────────────────────────
	var ngrokProc *os.Process
	if cfg.NgrokEnabled {
		proc, publicURL, err := startNgrok(cfg.WebhookPort, cfg.NgrokDomain)
		if err != nil {
			telemetry.Warnf("ngrok: failed to start: %v", err)
			telemetry.Warnf("ngrok: falling back to local-only on %s", addr)
		} else {
			ngrokProc = proc
			telemetry.Infof("ngrok tunnel: %s -> %s", publicURL, addr)
			telemetry.Infof("  GoalServe webhook URL: %s/webhook/hockey", publicURL)
		}
	}

	// ── Inbound: Kalshi WebSocket (optional) ─────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.KalshiAPIKey != "" {
		kalshiWS := kalshi_ws.NewClient(cfg.KalshiWSURL, cfg.KalshiAPIKey, bus)
		go func() {
			if err := kalshiWS.Connect(ctx); err != nil {
				telemetry.Warnf("kalshi_ws: failed to connect: %v", err)
			}
		}()
	}

	// ── Graceful shutdown ────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	telemetry.Infof("shutting down...")
	cancel()

	if ngrokProc != nil {
		telemetry.Infof("stopping ngrok...")
		ngrokProc.Signal(syscall.SIGTERM)
		ngrokProc.Wait()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	telemetry.Infof("shutdown complete  webhooks=%d events=%d scores=%d orders=%d errors=%d",
		telemetry.Metrics.WebhooksReceived.Value(),
		telemetry.Metrics.EventsProcessed.Value(),
		telemetry.Metrics.ScoreChanges.Value(),
		telemetry.Metrics.OrdersSent.Value(),
		telemetry.Metrics.OrderErrors.Value(),
	)
}

// startNgrok launches ngrok as a subprocess and returns the public URL.
// Queries ngrok's local API at localhost:4040 to discover the tunnel URL.
func startNgrok(port int, domain string) (*os.Process, string, error) {
	args := []string{"http", fmt.Sprintf("%d", port)}
	if domain != "" {
		args = append(args, "--domain", domain)
	}

	cmd := exec.Command("ngrok", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start ngrok: %w", err)
	}

	// Wait for ngrok to start and expose its API.
	var publicURL string
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)
		url, err := queryNgrokAPI()
		if err == nil && url != "" {
			publicURL = url
			break
		}
	}

	if publicURL == "" {
		cmd.Process.Kill()
		return nil, "", fmt.Errorf("ngrok started but no tunnel URL found after 15s")
	}

	return cmd.Process, publicURL, nil
}

func queryNgrokAPI() (string, error) {
	resp, err := http.Get("http://localhost:4040/api/tunnels")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Tunnels []struct {
			PublicURL string `json:"public_url"`
			Proto     string `json:"proto"`
		} `json:"tunnels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	for _, t := range result.Tunnels {
		if t.Proto == "https" {
			return t.PublicURL, nil
		}
	}
	if len(result.Tunnels) > 0 {
		return result.Tunnels[0].PublicURL, nil
	}
	return "", nil
}

// registerSportLanes creates per-league lanes that share a single sport-level
// SpendGuard. If no YAML config exists for a sport, it registers a permissive
// wildcard lane with defaults.
func registerSportLanes(router *execution.LaneRouter, rl config.RiskLimits, sport events.Sport, sportKey string) {
	sl, ok := rl.SportLimit(sportKey)
	if !ok {
		// No YAML entry — register a wildcard with generous defaults.
		router.Register(sport, "*", lanes.NewLane(5000, 50000, 500))
		return
	}

	sportSpend := lanes.NewSpendGuard(sl.MaxSportCents)

	if len(sl.Leagues) == 0 {
		router.Register(sport, "*", lanes.NewLaneWithSpend(5000, sportSpend, 500))
		return
	}

	for league, ll := range sl.Leagues {
		throttle := ll.ThrottleMs
		if throttle == 0 {
			throttle = 500
		}
		router.Register(sport, league, lanes.NewLaneWithSpend(ll.MaxGameCents, sportSpend, throttle))
	}

	// Wildcard fallback for leagues not explicitly listed.
	router.Register(sport, "*", lanes.NewLaneWithSpend(5000, sportSpend, 500))
}

func initLogging(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	telemetry.Init(l)
}
