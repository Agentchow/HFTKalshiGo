package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type LeagueLimits struct {
	MaxGameCents int `yaml:"max_game_cents"`
}

type SportLimits struct {
	MaxSportCents int                      `yaml:"max_sport_cents"`
	Leagues       map[string]LeagueLimits  `yaml:"leagues"`
}

type GlobalLimits struct {
	DefaultBankrollCents int `yaml:"default_bankroll_cents"`
}

type RiskLimits struct {
	Global GlobalLimits           `yaml:"global"`
	Sports map[string]SportLimits `yaml:"sports"`
}

func LoadRiskLimits(path string) (RiskLimits, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RiskLimits{}, fmt.Errorf("read risk limits: %w", err)
	}

	var limits RiskLimits
	if err := yaml.Unmarshal(data, &limits); err != nil {
		return RiskLimits{}, fmt.Errorf("parse risk limits: %w", err)
	}

	return limits, nil
}

func (rl RiskLimits) SportLimit(sport string) (SportLimits, bool) {
	sl, ok := rl.Sports[sport]
	return sl, ok
}

func (rl RiskLimits) LeagueLimit(sport, league string) (LeagueLimits, bool) {
	sl, ok := rl.Sports[sport]
	if !ok {
		return LeagueLimits{}, false
	}
	ll, ok := sl.Leagues[league]
	return ll, ok
}
