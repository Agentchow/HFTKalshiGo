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
	if eventType == "TICKER UPDATE" {
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
	if hs.HomeTicker != "" {
		fmt.Fprintf(&b, "    Kalshi  %-28sYes %2.0fc  |  No %2.0fc\n", homeShort+":", homeYes, homeNo)
	}
	if hs.AwayTicker != "" {
		fmt.Fprintf(&b, "            %-28sYes %2.0fc  |  No %2.0fc\n", awayShort+":", awayYes, awayNo)
	}
	if hs.HomeTicker != "" || hs.AwayTicker != "" {
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

func shortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return name
	}
	return parts[len(parts)-1]
}

func pctOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
