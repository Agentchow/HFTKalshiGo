package main

import (
	"io"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/goalserve_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	soccerStrat "github.com/charleschow/hft-trading/internal/core/strategy/soccer"
	"github.com/charleschow/hft-trading/internal/core/training"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/process"
)

func main() {
	process.Run(process.SportProcessConfig{
		Sport:    events.SportSoccer,
		SportKey: "soccer",
		BuildStrategy: func(cfg *config.Config) strategy.Strategy {
			var pregame soccerStrat.PregameOddsProvider
			if cfg.GoalserveAPIKey != "" {
				pregame = goalserve_http.NewPregameClient(cfg.GoalserveAPIKey)
			}
			return soccerStrat.NewStrategy(pregame)
		},
		ConfigureEngine: func(cfg *config.Config, engine *strategy.Engine) (io.Closer, error) {
			store, err := training.OpenStore(cfg.SoccerTrainingDBPath)
			if err != nil {
				return nil, err
			}
			engine.SetSoccerTraining(store, cfg.TrainingBackfillDelaySec)
			return store, nil
		},
	})
}
