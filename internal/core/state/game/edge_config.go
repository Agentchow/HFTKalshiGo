package game

import (
	_ "embed"

	"gopkg.in/yaml.v3"
)

//go:embed edge_config.yaml
var edgeConfigData []byte

var edgeThresholdPct = 3.0

func init() {
	var cfg struct {
		EdgeThresholdPct float64 `yaml:"edge_threshold_pct"`
	}
	if err := yaml.Unmarshal(edgeConfigData, &cfg); err == nil && cfg.EdgeThresholdPct > 0 {
		edgeThresholdPct = cfg.EdgeThresholdPct
	}
}

func EdgeThresholdPct() float64 { return edgeThresholdPct }
