package process

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charleschow/hft-trading/internal/adapters/inbound/kalshi_ws"
	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/display"
	"github.com/charleschow/hft-trading/internal/core/execution"
	"github.com/charleschow/hft-trading/internal/core/state/game"
	"github.com/charleschow/hft-trading/internal/core/state/store"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/fanout"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// SportProcessConfig captures the sport-specific pieces that differ
// between hockey, soccer, and football entry points.
type SportProcessConfig struct {
	Sport    events.Sport
	SportKey string // "soccer", "hockey", "football" — used for logs + risk lookup

	// BuildStrategy returns the sport-specific strategy implementation.
	BuildStrategy func(cfg *config.Config) strategy.Strategy

	// BuildTrainingObserver optionally creates a training observer.
	// The returned io.Closer (if non-nil) is closed on shutdown.
	BuildTrainingObserver func(cfg *config.Config) (game.GameObserver, io.Closer, error)
}

// Run boots a sport-specific trading process. It wires all shared
// infrastructure (Kalshi auth, HTTP client, ticker resolver, execution
// lanes, fanout client) and delegates sport-specific setup to the
// closures in spc.
func Run(spc SportProcessConfig) {
	cfg := config.Load()
	telemetry.Init(telemetry.ParseLogLevel(cfg.LogLevel))

	label := strings.ToUpper(spc.SportKey[:1]) + spc.SportKey[1:]
	telemetry.Infof("Starting %s process", spc.SportKey)

	bus := events.NewBus()
	gameStore := store.New()

	// ── Kalshi HTTP client ─────────────────────────────────────
	kalshiSigner, err := kalshi_auth.NewSignerFromFile(cfg.KalshiKeyID, cfg.KalshiKeyFile)
	if err != nil {
		telemetry.Errorf("Kalshi auth: %v", err)
		os.Exit(1)
	}
	kalshiClient := kalshi_http.NewClient(cfg.KalshiBaseURL, kalshiSigner, cfg.RateDivisor)

	if err := kalshiClient.WarmConnection(context.Background()); err != nil {
		telemetry.Warnf("Kalshi connection warm-up failed: %v", err)
	}

	balance, err := kalshiClient.GetBalance(context.Background())
	if err != nil {
		telemetry.Warnf("Balance fetch failed: %v", err)
	} else {
		telemetry.Infof("[Kalshi] balance: $%.2f", float64(balance)/100.0)
	}

	// ── Ticker resolver ────────────────────────────────────────
	tickerResolver := ticker.NewResolver(kalshiClient, cfg.TickersConfigDir, spc.Sport)

	// ── Kalshi WebSocket ──────────────────────────────────────
	kalshiWS := kalshi_ws.NewClient(cfg.KalshiWSURL, kalshiSigner, bus)

	// ── Strategy (sport-specific via closure) ──────────────────
	registry := strategy.NewRegistry()
	registry.Register(spc.Sport, spc.BuildStrategy(cfg))

	// ── Observers ──────────────────────────────────────────────
	var observers []game.GameObserver
	var trainingCloser io.Closer

	displayObs := display.NewObserver(func(sport events.Sport) (display.Displayer, bool) {
		return registry.Get(sport)
	})
	observers = append(observers, displayObs)

	if spc.BuildTrainingObserver != nil {
		obs, closer, err := spc.BuildTrainingObserver(cfg)
		if err != nil {
			telemetry.Errorf("%s training store: %v", label, err)
			os.Exit(1)
		}
		observers = append(observers, obs)
		trainingCloser = closer
	}
	if trainingCloser != nil {
		defer trainingCloser.Close()
	}

	// ── Engine ─────────────────────────────────────────────────
	_ = strategy.NewEngine(bus, gameStore, registry, tickerResolver, kalshiWS, observers)

	telemetry.Infof("Listening for %s games via fanout (%s) (GoalServe will take ~10 sec)...", spc.SportKey, cfg.FanoutAddr)

	// ── Execution ──────────────────────────────────────────────
	riskLimits, err := config.LoadRiskLimits(cfg.RiskLimitsPath)
	if err != nil {
		telemetry.Errorf("Failed to load risk limits: %v", err)
		os.Exit(1)
	}

	laneRouter := execution.NewLaneRouter()
	execution.RegisterLanesFromConfig(laneRouter, riskLimits, spc.Sport, spc.SportKey)
	_ = execution.NewService(bus, laneRouter, kalshiClient, gameStore)

	// ── Fanout client & Kalshi WS ─────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fanoutClient := fanout.NewClient(cfg.FanoutAddr, spc.Sport, bus)
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

	telemetry.Infof("Shutting down %s...", spc.SportKey)
	cancel()

	telemetry.Infof("%s shutdown complete  scores=%d  orders=%d  errors=%d",
		label,
		telemetry.Metrics.ScoreChanges.Value(),
		telemetry.Metrics.OrdersSent.Value(),
		telemetry.Metrics.OrderErrors.Value(),
	)
}
