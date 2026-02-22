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

	// Pinnacle
	pinnacle := fmt.Sprintf("%s %.1f%%  |  %s %.1f%%", homeShort, pctOrZero(hs.PinnacleHomePct), awayShort, pctOrZero(hs.PinnacleAwayPct))
	if hs.PinnacleHomePct == nil || hs.PinnacleAwayPct == nil {
		pinnacle = "(not available)"
	}

	// Best odds = yes ask for each side
	bestHome := homeYes
	bestAway := awayYes

	var b strings.Builder
	fmt.Fprintf(&b, "\n[%s %s]\n", eventType, ts)
	fmt.Fprintf(&b, "%s\n", divider)
	fmt.Fprintf(&b, "  %s @ %s\n", hs.AwayTeam, hs.HomeTeam)
	fmt.Fprintf(&b, "    %-38s%s %.1f%%  |  %s %.1f%%\n",
		"Pregame strength (Goalserve):", homeShort, hs.HomeWinPct*100, awayShort, hs.AwayWinPct*100)
	fmt.Fprintf(&b, "    %-38sScore %d-%d  |  Period %s (~%.0f min left)\n",
		"Score & time (Goalserve):", hs.HomeScore, hs.AwayScore, hs.Period, hs.TimeLeft)
	hasKalshi := homeYes > 0 || homeNo > 0 || awayYes > 0 || awayNo > 0
	if hs.HomeTicker != "" {
		if hasKalshi {
			fmt.Fprintf(&b, "    Kalshi  %-28sYes %2.0fc  |  No %2.0fc\n", homeShort+":", homeYes, homeNo)
		} else {
			fmt.Fprintf(&b, "    Kalshi  %-28sYes  —   |  No  —\n", homeShort+":")
		}
	}
	if hs.AwayTicker != "" {
		if hasKalshi {
			fmt.Fprintf(&b, "            %-28sYes %2.0fc  |  No %2.0fc\n", awayShort+":", awayYes, awayNo)
		} else {
			fmt.Fprintf(&b, "            %-28sYes  —   |  No  —\n", awayShort+":")
		}
	}
	if (hs.HomeTicker != "" || hs.AwayTicker != "") && hasKalshi {
		fmt.Fprintf(&b, "    %-38s%s: %.0fc  |  %s %.0fc\n",
			"Best odds:", homeShort, bestHome, awayShort, bestAway)
	}
	fmt.Fprintf(&b, "    %-38s%s\n", "Pinnacle:", pinnacle)
	if hs.ModelHomePct > 0 || hs.ModelAwayPct > 0 {
		fmt.Fprintf(&b, "    %-38s%s %.1f%%  |  %s %.1f%%\n",
			"Model:", homeShort, hs.ModelHomePct, awayShort, hs.ModelAwayPct)
	} else {
		fmt.Fprintf(&b, "    %-38s%s\n", "Model:", "(not computed)")
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
