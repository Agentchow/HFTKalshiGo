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
	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
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
	telemetry.Init(parseLogLevel(cfg.LogLevel))
	telemetry.Infof("Starting system")

	bus := events.NewBus()
	gameStore := store.New()

	// ── Risk limits ─────────────────────────────────────────────
	riskLimits, err := config.LoadRiskLimits(cfg.RiskLimitsPath)
	if err != nil {
		telemetry.Errorf("Failed to load risk limits: %v", err)
		os.Exit(1)
	}

	// ── Strategies ──────────────────────────────────────────────
	registry := strategy.NewRegistry()
	registry.Register(events.SportHockey, hockeyStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	registry.Register(events.SportSoccer, soccerStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	registry.Register(events.SportFootball, footballStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	_ = strategy.NewEngine(bus, gameStore, registry)

	// ── Execution lanes ─────────────────────────────────────────
	laneRouter := execution.NewLaneRouter()
	registerSportLanes(laneRouter, riskLimits, events.SportHockey, "hockey")
	registerSportLanes(laneRouter, riskLimits, events.SportSoccer, "soccer")
	registerSportLanes(laneRouter, riskLimits, events.SportFootball, "football")

	// ── Kalshi auth + clients ───────────────────────────────────
	kalshiSigner, err := kalshi_auth.NewSignerFromFile(cfg.KalshiKeyID, cfg.KalshiKeyFile)
	if err != nil {
		telemetry.Errorf("Kalshi auth: %v", err)
		os.Exit(1)
	}
	if !kalshiSigner.Enabled() {
		telemetry.Errorf("Kalshi credentials missing — set %s_KEYID and %s_KEYFILE in .env", cfg.KalshiMode, cfg.KalshiMode)
		os.Exit(1)
	}
	telemetry.Infof("Kalshi connected  mode=%s  api=%s", cfg.KalshiMode, cfg.KalshiBaseURL)

	kalshiClient := kalshi_http.NewClient(cfg.KalshiBaseURL, kalshiSigner)
	_ = execution.NewService(bus, laneRouter, kalshiClient, gameStore)

	// ── Webhook payload store ───────────────────────────────────
	webhookStore, err := goalserve_webhook.OpenStore(cfg.WebhookStorePath)
	if err != nil {
		telemetry.Warnf("Webhook store disabled: %v", err)
	}

	// ── Webhook server ──────────────────────────────────────────
	webhookHandler := goalserve_webhook.NewHandler(bus, webhookStore)
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

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			telemetry.Errorf("HTTP server: %v", err)
			os.Exit(1)
		}
	}()
	telemetry.Infof("Webhook listening on %q", addr)

	// ── ngrok ───────────────────────────────────────────────────
	var ngrokProc *os.Process
	if cfg.NgrokEnabled {
		proc, publicURL, err := startNgrok(cfg.WebhookPort, cfg.NgrokAuthToken, cfg.NgrokDomain)
		if err != nil {
			telemetry.Warnf("Ngrok failed: %v (falling back to local)", err)
		} else {
			ngrokProc = proc
			telemetry.Infof("Ngrok tunnel on %q", publicURL)
		}
	}

	// ── Kalshi WebSocket ────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kalshiWS := kalshi_ws.NewClient(cfg.KalshiWSURL, kalshiSigner, bus)
	go func() {
		if err := kalshiWS.Connect(ctx); err != nil {
			telemetry.Warnf("Kalshi WS: %v", err)
		}
	}()

	// ── Shutdown ────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	telemetry.Infof("Shutting down...")
	cancel()

	if ngrokProc != nil {
		ngrokProc.Signal(syscall.SIGTERM)
		ngrokProc.Wait()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	if webhookStore != nil {
		webhookStore.Close()
	}

	telemetry.Infof("Shutdown complete  webhooks=%d  events=%d  scores=%d  orders=%d  errors=%d",
		telemetry.Metrics.WebhooksReceived.Value(),
		telemetry.Metrics.EventsProcessed.Value(),
		telemetry.Metrics.ScoreChanges.Value(),
		telemetry.Metrics.OrdersSent.Value(),
		telemetry.Metrics.OrderErrors.Value(),
	)
}

func startNgrok(port int, authToken, domain string) (*os.Process, string, error) {
	args := []string{"http", fmt.Sprintf("%d", port)}
	if authToken != "" {
		args = append(args, "--authtoken", authToken)
	}
	if domain != "" {
		args = append(args, "--domain", domain)
	}

	cmd := exec.Command("ngrok", args...)
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start ngrok: %w", err)
	}

	var publicURL string
	for i := 0; i < 30; i++ {
		time.Sleep(500 * time.Millisecond)
		if u, err := queryNgrokAPI(); err == nil && u != "" {
			publicURL = u
			break
		}
	}
	if publicURL == "" {
		cmd.Process.Kill()
		return nil, "", fmt.Errorf("no tunnel URL after 15s")
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

func registerSportLanes(router *execution.LaneRouter, rl config.RiskLimits, sport events.Sport, sportKey string) {
	sl, ok := rl.SportLimit(sportKey)
	if !ok {
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
	router.Register(sport, "*", lanes.NewLaneWithSpend(5000, sportSpend, 500))
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
