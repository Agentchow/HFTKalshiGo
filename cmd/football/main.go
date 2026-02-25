package main

import (
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/charleschow/hft-trading/internal/core/strategy"
	footballStrat "github.com/charleschow/hft-trading/internal/core/strategy/football"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/process"
)

func main() {
	process.Run(process.SportProcessConfig{
		Sport:    events.SportFootball,
		SportKey: "football",
		BuildStrategy: func(cfg *config.Config) strategy.Strategy {
			return footballStrat.NewStrategy()
		},
	})
}
