package main

import (
	"io"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/goalserve_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/state/game"
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
		BuildTrainingObserver: func(cfg *config.Config) (game.GameObserver, io.Closer, error) {
			store, err := training.OpenStore(cfg.SoccerTrainingDBPath)
			if err != nil {
				return nil, nil, err
			}
			return training.NewSoccerObserver(store, cfg.TrainingBackfillDelaySec), store, nil
		},
	})
}
