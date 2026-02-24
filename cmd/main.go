package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/inbound/goalserve_webhook"
	goalserve_ws "github.com/charleschow/hft-trading/internal/adapters/inbound/goalserve_ws"
	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/fanout"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

func main() {
	cfg := config.Load()
	telemetry.Init(telemetry.ParseLogLevel(cfg.LogLevel))
	telemetry.Infof("Starting central infrastructure")

	bus := events.NewBus()

	// ── Kalshi auth ────────────────────────────────────────────
	kalshiSigner, err := kalshi_auth.NewSignerFromFile(cfg.KalshiKeyID, cfg.KalshiKeyFile)
	if err != nil {
		telemetry.Errorf("Kalshi auth: %v", err)
		os.Exit(1)
	}
	if !kalshiSigner.Enabled() {
		telemetry.Errorf("Kalshi credentials missing — set %s_KEYID and %s_KEYFILE in .env", cfg.KalshiMode, cfg.KalshiMode)
		os.Exit(1)
	}
	telemetry.Plainf("Kalshi connected  mode=%s  api=%s", cfg.KalshiMode, cfg.KalshiBaseURL)

	// ── Balance fetch ──────────────────────────────────────────
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

	// ── Fanout server ──────────────────────────────────────────
	fanoutServer := fanout.NewServer(bus)
	go func() {
		if err := fanoutServer.ListenAndServe(cfg.FanoutPort); err != nil {
			telemetry.Errorf("Fanout server: %v", err)
			os.Exit(1)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var server *http.Server
	var ngrokProc *os.Process
	var webhookStore *goalserve_webhook.Store
	var wsStore *goalserve_ws.Store

	if cfg.GoalserveWSEnabled {
		// ── GoalServe WebSocket mode ──────────────────────────────
		var err2 error
		wsStore, err2 = goalserve_ws.OpenStore(cfg.GoalserveWSStorePath)
		if err2 != nil {
			telemetry.Warnf("WS store disabled: %v", err2)
		}

		tp := goalserve_ws.NewTokenProvider(cfg.GoalserveWSAuthURL, cfg.GoalserveAPIKey)

		sports := strings.Split(cfg.GoalserveWSSports, ",")
		telemetry.Plainf("GoalServe WS mode enabled  sports=%v", sports)

		for _, sport := range sports {
			sport = strings.TrimSpace(sport)
			if sport == "" {
				continue
			}
			wsClient := goalserve_ws.NewClient(sport, cfg.GoalserveWSURL, tp, bus, wsStore)
			go wsClient.ConnectWithRetry(ctx)
		}
	} else {
		// ── Webhook mode (default) ────────────────────────────────
		webhookStore, err = goalserve_webhook.OpenStore(cfg.WebhookStorePath)
		if err != nil {
			telemetry.Warnf("Webhook store disabled: %v", err)
		}

		webhookHandler := goalserve_webhook.NewHandler(bus, webhookStore)
		mux := http.NewServeMux()
		webhookHandler.RegisterRoutes(mux)

		addr := fmt.Sprintf("%s:%d", cfg.WebhookHost, cfg.WebhookPort)
		server = &http.Server{
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
		telemetry.Plainf("Webhook listening on %q", addr)

		if cfg.NgrokEnabled {
			proc, publicURL, err := startNgrok(cfg.WebhookPort, cfg.NgrokAuthToken, cfg.NgrokDomain)
			if err != nil {
				telemetry.Warnf("Ngrok failed: %v (falling back to local)", err)
			} else {
				ngrokProc = proc
				telemetry.Plainf("Ngrok tunnel on %q", publicURL)
			}
		}
	}

	// ── Shutdown ───────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	telemetry.Infof("Shutting down...")
	cancel()

	if ngrokProc != nil {
		ngrokProc.Signal(syscall.SIGTERM)
		ngrokProc.Wait()
	}

	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}

	if webhookStore != nil {
		webhookStore.Close()
	}
	if wsStore != nil {
		wsStore.Close()
	}

	if cfg.GoalserveWSEnabled {
		telemetry.Infof("Shutdown complete  ws_msgs=%d  events=%d  reconnects=%d",
			telemetry.Metrics.WSMessagesReceived.Value(),
			telemetry.Metrics.EventsProcessed.Value(),
			telemetry.Metrics.WSReconnects.Value(),
		)
	} else {
		telemetry.Infof("Shutdown complete  webhooks=%d  events=%d",
			telemetry.Metrics.WebhooksReceived.Value(),
			telemetry.Metrics.EventsProcessed.Value(),
		)
	}
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
