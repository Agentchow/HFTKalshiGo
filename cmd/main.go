package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	hockeyStrat "github.com/charleschow/hft-trading/internal/core/strategy/hockey"
	soccerStrat "github.com/charleschow/hft-trading/internal/core/strategy/soccer"
	footballStrat "github.com/charleschow/hft-trading/internal/core/strategy/football"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

func main() {
	cfg := config.Load()
	initLogging(cfg.LogLevel)

	telemetry.Infof("starting hft-trading system")

	// ── Core infrastructure ──────────────────────────────────────
	bus := events.NewBus()
	gameStore := store.New()

	// ── Strategy registry ────────────────────────────────────────
	registry := strategy.NewRegistry()
	registry.Register(events.SportHockey, hockeyStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	registry.Register(events.SportSoccer, soccerStrat.NewStrategy(cfg.ScoreDropConfirmSec))
	registry.Register(events.SportFootball, footballStrat.NewStrategy(cfg.ScoreDropConfirmSec))

	// ── Strategy engine (subscribes to score changes) ────────────
	_ = strategy.NewEngine(bus, gameStore, registry)

	// ── Execution lane router ────────────────────────────────────
	laneRouter := execution.NewLaneRouter()
	laneRouter.Register(events.SportHockey, "*", lanes.NewLane(5, 5000, 500))
	laneRouter.Register(events.SportSoccer, "*", lanes.NewLane(6, 4000, 500))
	laneRouter.Register(events.SportFootball, "*", lanes.NewLane(5, 4000, 500))

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

	// ── Start HTTP server ────────────────────────────────────────
	go func() {
		telemetry.Infof("webhook server listening on %s", addr)
		telemetry.Infof("  POST /webhook/hockey")
		telemetry.Infof("  POST /webhook/soccer")
		telemetry.Infof("  POST /webhook/football")
		telemetry.Infof("  GET  /health")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			telemetry.Errorf("http server error: %v", err)
			os.Exit(1)
		}
	}()

	// ── Graceful shutdown ────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	telemetry.Infof("shutting down...")
	cancel()

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
