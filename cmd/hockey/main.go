package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/charleschow/hft-trading/internal/adapters/inbound/kalshi_ws"
	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/goalserve_http"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/execution"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	hockeyStrat "github.com/charleschow/hft-trading/internal/core/strategy/hockey"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/fanout"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

func main() {
	cfg := config.Load()
	telemetry.Init(telemetry.ParseLogLevel(cfg.LogLevel))
	telemetry.Infof("Starting hockey process")

	bus := events.NewBus()
	gameStore := store.New()

	// ── Kalshi HTTP client ─────────────────────────────────────
	kalshiSigner, err := kalshi_auth.NewSignerFromFile(cfg.KalshiKeyID, cfg.KalshiKeyFile)
	if err != nil {
		telemetry.Errorf("Kalshi auth: %v", err)
		os.Exit(1)
	}
	kalshiClient := kalshi_http.NewClient(cfg.KalshiBaseURL, kalshiSigner, cfg.RateDivisor)

	balance, err := kalshiClient.GetBalance(context.Background())
	if err != nil {
		telemetry.Warnf("Balance fetch failed: %v", err)
	} else {
		telemetry.Infof("Kalshi balance: $%.2f", float64(balance)/100.0)
	}

	// ── Ticker resolver ────────────────────────────────────────
	tickerResolver := ticker.NewResolver(kalshiClient, cfg.TickersConfigDir, events.SportHockey)

	// ── Pregame odds ──────────────────────────────────────────
	var pregame hockeyStrat.PregameOddsProvider
	if cfg.GoalserveAPIKey != "" {
		pregame = goalserve_http.NewPregameClient(cfg.GoalserveAPIKey)
	}

	// ── Kalshi WebSocket ──────────────────────────────────────
	kalshiWS := kalshi_ws.NewClient(cfg.KalshiWSURL, kalshiSigner, bus)

	// ── Strategy ───────────────────────────────────────────────
	registry := strategy.NewRegistry()
	registry.Register(events.SportHockey, hockeyStrat.NewStrategy(cfg.ScoreDropConfirmSec, pregame))
	_ = strategy.NewEngine(bus, gameStore, registry, tickerResolver, kalshiWS)

	// ── Execution ──────────────────────────────────────────────
	riskLimits, err := config.LoadRiskLimits(cfg.RiskLimitsPath)
	if err != nil {
		telemetry.Errorf("Failed to load risk limits: %v", err)
		os.Exit(1)
	}

	laneRouter := execution.NewLaneRouter()
	registerLanes(laneRouter, riskLimits, events.SportHockey, "hockey")
	_ = execution.NewService(bus, laneRouter, kalshiClient, gameStore)

	// ── Fanout client & Kalshi WS ─────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fanoutClient := fanout.NewClient(cfg.FanoutAddr, events.SportHockey, bus)
	go fanoutClient.ConnectWithRetry(ctx)

	go func() {
		if err := kalshiWS.Connect(ctx); err != nil {
			telemetry.Warnf("Kalshi WS: %v", err)
		}
	}()

	// ── Shutdown ───────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	telemetry.Infof("Shutting down hockey...")
	cancel()

	telemetry.Infof("Hockey shutdown complete  scores=%d  orders=%d  errors=%d",
		telemetry.Metrics.ScoreChanges.Value(),
		telemetry.Metrics.OrdersSent.Value(),
		telemetry.Metrics.OrderErrors.Value(),
	)
}

func registerLanes(router *execution.LaneRouter, rl config.RiskLimits, sport events.Sport, sportKey string) {
	sl, ok := rl.SportLimit(sportKey)
	if !ok {
		execution.RegisterSportLanes(router, 50000, nil, sport)
		return
	}
	leagues := make(map[string]int, len(sl.Leagues))
	for league, ll := range sl.Leagues {
		leagues[league] = ll.MaxGameCents
	}
	execution.RegisterSportLanes(router, sl.MaxSportCents, leagues, sport)
}
