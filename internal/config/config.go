package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// GoalServe webhook
	WebhookHost     string
	WebhookPort     int
	GoalserveAPIKey string

	// Kalshi API
	KalshiMode    string // "demo" or "prod"
	KalshiBaseURL string
	KalshiWSURL   string
	KalshiKeyID   string
	KalshiKeyFile string // path to RSA PEM private key

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
	NgrokDomain  string

	// Telemetry
	LogLevel string
}

func Load() *Config {
	_ = godotenv.Load()

	mode := envStr("KALSHI_MODE", "prod")

	var keyID, keyFile, baseURL, wsURL string
	if mode == "prod" {
		keyID = envStr("PROD_KEYID", "")
		keyFile = envStr("PROD_KEYFILE", "")
		baseURL = envStr("KALSHI_BASE_URL", "https://api.elections.kalshi.com")
		wsURL = envStr("KALSHI_WS_URL", "wss://api.elections.kalshi.com/trade-api/ws/v2")
	} else {
		keyID = envStr("DEMO_KEYID", "")
		keyFile = envStr("DEMO_KEYFILE", "")
		baseURL = envStr("KALSHI_BASE_URL", "https://demo-api.kalshi.co")
		wsURL = envStr("KALSHI_WS_URL", "wss://demo-api.kalshi.co/trade-api/ws/v2")
	}

	return &Config{
		WebhookHost:     envStr("GOALSERVE_WEBHOOK_HOST", "0.0.0.0"),
		WebhookPort:     envInt("GOALSERVE_WEBHOOK_PORT", 8765),
		GoalserveAPIKey: envStr("GOALSERVE_API_KEY", ""),

		KalshiMode:    mode,
		KalshiBaseURL: baseURL,
		KalshiWSURL:   wsURL,
		KalshiKeyID:   keyID,
		KalshiKeyFile: keyFile,

		GeniusWSURL: envStr("GENIUS_WS_URL", ""),
		GeniusToken: envStr("GENIUS_TOKEN", ""),

		RiskLimitsPath: envStr("RISK_LIMITS_PATH", "internal/config/risk_limits.yaml"),

		// Sommetimes GoalServe/GeniusScore will give us a score change where score "decreases"
		// Meaning the home team scored a Goal and the Referee decided to overturn it.
		// We wait X seconds to confirm a score actually dropped (overturned goal).
		ScoreDropConfirmSec: envInt("SCORE_DROP_CONFIRM_SEC", 30),
		// Pauses trading for X seconds due to score reset
		ScoreResetThrottle: time.Duration(envInt("SCORE_RESET_THROTTLE_SEC", 60)) * time.Second,

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
