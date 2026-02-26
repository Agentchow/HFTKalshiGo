package display

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
)

const (
	dividerHeavy = "========================================================================"
	dividerLight = "~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~"
)

func PrintHockey(gc *game.GameContext, eventType string) {
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return
	}

	divider := dividerHeavy
	if eventType == "EDGE" {
		divider = dividerLight
	}

	ts := time.Now().Format("3:04:05.000 PM")

	homeShort := shortName(hs.HomeTeam)
	awayShort := shortName(hs.AwayTeam)

	// Kalshi prices
	var homeYes, homeNo, awayYes, awayNo float64
	if td, ok := gc.Tickers[hs.HomeTicker]; ok {
		homeYes = td.YesAsk
		homeNo = td.NoAsk
	}
	if td, ok := gc.Tickers[hs.AwayTicker]; ok {
		awayYes = td.YesAsk
		awayNo = td.NoAsk
	}

	hasTicker := hs.HomeTicker != "" || hs.AwayTicker != ""

	wsTag := ""
	if !gc.KalshiConnected && len(gc.Tickers) > 0 {
		wsTag = "  [WS DOWN]"
	}

	titleEvent := eventType
	if eventType == "POWER PLAY" {
		if hs.IsHomePowerPlay {
			titleEvent = fmt.Sprintf("%s (%s)", eventType, homeShort)
		} else if hs.IsAwayPowerPlay {
			titleEvent = fmt.Sprintf("%s (%s)", eventType, awayShort)
		}
	}

	var b strings.Builder
	if gc.KalshiEventURL != "" {
		fmt.Fprintf(&b, "\n[%s %s]  %s%s\n", titleEvent, ts, gc.KalshiEventURL, wsTag)
	} else {
		fmt.Fprintf(&b, "\n[%s %s]%s\n", titleEvent, ts, wsTag)
	}
	fmt.Fprintf(&b, "%s\n", divider)
	fmt.Fprintf(&b, "  %s vs %s\n", hs.HomeTeam, hs.AwayTeam)
	fmt.Fprintf(&b, "    %-38s%s %.1f%%  |  %s %.1f%%\n",
		"Pregame strength (Goalserve):", homeShort, hs.HomeStrength*100, awayShort, hs.AwayStrength*100)
	ppTag := ""
	if hs.IsHomePowerPlay {
		ppTag = fmt.Sprintf("  [%s PP]", homeShort)
	} else if hs.IsAwayPowerPlay {
		ppTag = fmt.Sprintf("  [%s PP]", awayShort)
	}
	fmt.Fprintf(&b, "    %-38sScore %d-%d  |  Period %s (~%s left)%s\n",
		"Score & time (Goalserve):", hs.HomeScore, hs.AwayScore, hs.Period, fmtTimeLeft(hs.TimeLeft), ppTag)
	bestHome := homeYes
	bestAway := awayYes

	hasKalshi := homeYes > 0 || homeNo > 0 || awayYes > 0 || awayNo > 0
	if !gc.KalshiConnected && len(gc.Tickers) > 0 {
		fmt.Fprintf(&b, "    *** Kalshi WS disconnected — prices stale ***\n")
	}
	if hasTicker {
		if hasKalshi {
			fmt.Fprintf(&b, "    Kalshi  %-30sYes %2.0fc  |  No %2.0fc\n", homeShort+":", homeYes, homeNo)
			fmt.Fprintf(&b, "            %-30sYes %2.0fc  |  No %2.0fc\n", awayShort+":", awayYes, awayNo)
			fmt.Fprintf(&b, "    %-38s%s: %.0fc  |  %s %.0fc\n",
				"Best odds:", homeShort, bestHome, awayShort, bestAway)
		} else {
			fmt.Fprintf(&b, "    Kalshi  %-30sYes  —   |  No  —\n", homeShort+":")
			fmt.Fprintf(&b, "            %-30sYes  —   |  No  —\n", awayShort+":")
		}
	}
	if hs.ModelHomePct > 0 || hs.ModelAwayPct > 0 {
		fmt.Fprintf(&b, "    %-38s%s %.1f%%  |  %s %.1f%%\n",
			"Model:", homeShort, hs.ModelHomePct, awayShort, hs.ModelAwayPct)
	} else {
		fmt.Fprintf(&b, "    %-38s%s\n", "Model:", "(not computed)")
	}
	if hasKalshi && (hs.ModelHomePct > 0 || hs.ModelAwayPct > 0) {
		var edges []string
		for _, e := range []struct {
			name string
			side string
			val  float64
		}{
			{homeShort, "YES", hs.EdgeHomeYes},
			{awayShort, "YES", hs.EdgeAwayYes},
			{homeShort, "NO", hs.EdgeHomeNo},
			{awayShort, "NO", hs.EdgeAwayNo},
		} {
			if e.val >= 3.0 {
				edges = append(edges, fmt.Sprintf("%s %s %+.1f%%", e.name, e.side, e.val))
			}
		}
		if len(edges) > 0 {
			fmt.Fprintf(&b, "    >>> %s\n", strings.Join(edges, " | "))
		}
	}
	fmt.Fprintf(&b, "%s\n", divider)

	fmt.Fprint(os.Stderr, b.String())
}

var teamSuffixes = map[string]bool{
	"FC": true, "SC": true, "CF": true, "AFC": true, "FK": true,
	"BK": true, "IF": true, "SK": true, "CD": true, "AD": true,
	"UD": true, "SV": true, "CA": true, "RC": true,
}

func shortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return name
	}
	last := parts[len(parts)-1]
	if len(parts) > 1 && teamSuffixes[strings.ToUpper(last)] {
		return parts[len(parts)-2]
	}
	return last
}

func pctOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func fmtTimeLeft(minutes float64) string {
	totalSec := int(minutes*60 + 0.5)
	m := totalSec / 60
	s := totalSec % 60
	return fmt.Sprintf("%dm %ds", m, s)
}
