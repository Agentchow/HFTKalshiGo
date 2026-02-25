package main

import (
	"io"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/goalserve_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	hockeyStrat "github.com/charleschow/hft-trading/internal/core/strategy/hockey"
	"github.com/charleschow/hft-trading/internal/core/training"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/process"
)

func main() {
	process.Run(process.SportProcessConfig{
		Sport:    events.SportHockey,
		SportKey: "hockey",
		BuildStrategy: func(cfg *config.Config) strategy.Strategy {
			var pregame hockeyStrat.PregameOddsProvider
			if cfg.GoalserveAPIKey != "" {
				pregame = goalserve_http.NewPregameClient(cfg.GoalserveAPIKey)
			}
			return hockeyStrat.NewStrategy(pregame)
		},
		ConfigureEngine: func(cfg *config.Config, engine *strategy.Engine) (io.Closer, error) {
			store, err := training.OpenHockeyStore(cfg.HockeyTrainingDBPath)
			if err != nil {
				return nil, err
			}
			engine.SetHockeyTraining(store, cfg.TrainingBackfillDelaySec)
			return store, nil
		},
	})
}
