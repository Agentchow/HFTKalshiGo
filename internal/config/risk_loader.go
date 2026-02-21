package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type LeagueLimits struct {
	MaxGameCents int   `yaml:"max_game_cents"`
	ThrottleMs   int64 `yaml:"throttle_ms"`
}

type SportLimits struct {
	MaxSportCents int                      `yaml:"max_sport_cents"`
	Leagues       map[string]LeagueLimits  `yaml:"leagues"`
}

type RiskLimits map[string]SportLimits

func LoadRiskLimits(path string) (RiskLimits, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read risk limits: %w", err)
	}

	var limits RiskLimits
	if err := yaml.Unmarshal(data, &limits); err != nil {
		return nil, fmt.Errorf("parse risk limits: %w", err)
	}

	return limits, nil
}

func (rl RiskLimits) SportLimit(sport string) (SportLimits, bool) {
	sl, ok := rl[sport]
	return sl, ok
}

func (rl RiskLimits) LeagueLimit(sport, league string) (LeagueLimits, bool) {
	sl, ok := rl[sport]
	if !ok {
		return LeagueLimits{}, false
	}
	ll, ok := sl.Leagues[league]
	return ll, ok
}
