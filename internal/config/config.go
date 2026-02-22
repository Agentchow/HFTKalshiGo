package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// GoalServe webhook
	WebhookHost string
	WebhookPort int

	// Kalshi API
	KalshiBaseURL string
	KalshiWSURL   string
	KalshiAPIKey  string
	KalshiSecret  string

	// Genius Sports
	GeniusWSURL string
	GeniusToken string

	// Risk
	RiskLimitsPath string

	// Timing
	ScoreDropConfirmSec int
	ScoreResetThrottle  time.Duration

	// ngrok
	NgrokEnabled bool
	NgrokDomain  string // custom domain, e.g. "myapp.ngrok.io"

	// Telemetry
	LogLevel string
}

func Load() *Config {
	_ = godotenv.Load()

	return &Config{
		WebhookHost: envStr("GOALSERVE_WEBHOOK_HOST", "0.0.0.0"),
		WebhookPort: envInt("GOALSERVE_WEBHOOK_PORT", 8765),

		KalshiBaseURL: envStr("KALSHI_BASE_URL", "https://trading-api.kalshi.com"),
		KalshiWSURL:   envStr("KALSHI_WS_URL", "wss://trading-api.kalshi.com/trade-api/ws/v2"),
		KalshiAPIKey:  envStr("KALSHI_API_KEY", ""),
		KalshiSecret:  envStr("KALSHI_SECRET", ""),

		GeniusWSURL: envStr("GENIUS_WS_URL", ""),
		GeniusToken: envStr("GENIUS_TOKEN", ""),

		RiskLimitsPath: envStr("RISK_LIMITS_PATH", "internal/config/risk_limits.yaml"),

		// Sometimes GoalServe/GeniusScore will give us a score change where score "decreases"
		// Meaning the home team scored a Goal and the Referee decided to overturn it.
		// We want to wait a few seconds to confirm the score drop before placing an order.
		ScoreDropConfirmSec: envInt("SCORE_DROP_CONFIRM_SEC", 30),
		ScoreResetThrottle:  time.Duration(envInt("SCORE_RESET_THROTTLE_SEC", 60)) * time.Second,

		NgrokEnabled: envStr("NGROK_ENABLED", "true") == "true",
		NgrokDomain:  envStr("NGROK_DOMAIN", ""),

		LogLevel: envStr("LOG_LEVEL", "info"),
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
