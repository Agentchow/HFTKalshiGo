package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// GoalServe webhook
	WebhookHost      string
	WebhookPort      int
	GoalserveAPIKey  string
	WebhookStorePath string

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

	// Timing — wait X seconds to confirm an overturned goal before acting.
	ScoreDropConfirmSec int
	ScoreResetThrottle  time.Duration

	// ngrok
	NgrokEnabled   bool
	NgrokAuthToken string
	NgrokDomain    string

	// Fanout (inter-process relay)
	FanoutPort int    // port the central fanout server listens on
	FanoutAddr string // address sport processes connect to

	// Rate limiting
	RateDivisor int // divide Kalshi rate limits by this (set to N when running N sport processes)

	// Tickers
	TickersConfigDir string // path to directory containing {Sport}/tickers_config.json files

	// Training
	SoccerTrainingDBPath     string
	HockeyTrainingDBPath     string
	TrainingBackfillDelaySec int

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
		WebhookHost:      envStr("GOALSERVE_WEBHOOK_HOST", "0.0.0.0"),
		WebhookPort:      envInt("GOALSERVE_WEBHOOK_PORT", 8765),
		GoalserveAPIKey:  envStr("GOALSERVE_API_KEY", ""),
		WebhookStorePath: envStr("WEBHOOK_STORE_PATH", "data/goalserve_webhooks.db"),

		KalshiMode:    mode,
		KalshiBaseURL: baseURL,
		KalshiWSURL:   wsURL,
		KalshiKeyID:   keyID,
		KalshiKeyFile: keyFile,

		GeniusWSURL: envStr("GENIUS_WS_URL", ""),
		GeniusToken: envStr("GENIUS_TOKEN", ""),

		RiskLimitsPath: envStr("RISK_LIMITS_PATH", "internal/config/risk_limits.yaml"),

		ScoreDropConfirmSec: envInt("SCORE_DROP_CONFIRM_SEC", 15),
		ScoreResetThrottle:  time.Duration(envInt("SCORE_RESET_THROTTLE_SEC", 5)) * time.Second,

		NgrokEnabled:   envStr("NGROK_ENABLED", "true") == "true",
		NgrokAuthToken: envStr("NGROK_AUTH_TOKEN", ""),
		NgrokDomain:    envStr("NGROK_DOMAIN", ""),

		FanoutPort:  envInt("FANOUT_PORT", 9100),
		FanoutAddr:  envStr("FANOUT_ADDR", "localhost:9100"),
		RateDivisor: envInt("RATE_DIVISOR", 1),

		TickersConfigDir: envStr("TICKERS_CONFIG_DIR", "configs"),

		SoccerTrainingDBPath:     envStr("SOCCER_TRAINING_DB_PATH", "data/soccer_training.db"),
		HockeyTrainingDBPath:     envStr("HOCKEY_TRAINING_DB_PATH", "data/hockey_training.db"),
		TrainingBackfillDelaySec: envInt("TRAINING_BACKFILL_DELAY_SEC", 10),

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
