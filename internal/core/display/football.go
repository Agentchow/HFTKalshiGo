package display

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	fbState "github.com/charleschow/hft-trading/internal/core/state/game/football"
)

func PrintFootball(gc *game.GameContext, eventType string) {
	fs, ok := gc.Game.(*fbState.FootballState)
	if !ok {
		return
	}

	divider := dividerHeavy
	if eventType == "EDGE" {
		divider = dividerLight
	}

	ts := time.Now().Format("3:04:05.000 PM")

	homeShort := shortName(fs.HomeTeam)
	awayShort := shortName(fs.AwayTeam)

	var homeYes, homeNo, awayYes, awayNo float64
	if td, ok := gc.Tickers[fs.HomeTicker]; ok {
		homeYes = td.YesAsk
		homeNo = td.NoAsk
	}
	if td, ok := gc.Tickers[fs.AwayTicker]; ok {
		awayYes = td.YesAsk
		awayNo = td.NoAsk
	}

	bestHome := homeYes
	bestAway := awayYes

	var b strings.Builder
	fmt.Fprintf(&b, "\n[%s %s]\n", eventType, ts)
	fmt.Fprintf(&b, "%s\n", divider)
	fmt.Fprintf(&b, "  %s @ %s\n", fs.AwayTeam, fs.HomeTeam)
	fmt.Fprintf(&b, "    %-38s%s %.1f%%  |  %s %.1f%%\n",
		"Pregame strength (Goalserve):", homeShort, fs.HomePregameStrength*100, awayShort, fs.AwayPregameStrength*100)
	fmt.Fprintf(&b, "    %-38sScore %d-%d  |  Quarter %s (%.0f min left)\n",
		"Score & time (Goalserve):", fs.HomeScore, fs.AwayScore, fs.Quarter, fs.TimeLeft)
	if fs.HomeTicker != "" {
		fmt.Fprintf(&b, "    Kalshi  %-28sYes %2.0fc  |  No %2.0fc\n", homeShort+":", homeYes, homeNo)
	}
	if fs.AwayTicker != "" {
		fmt.Fprintf(&b, "            %-28sYes %2.0fc  |  No %2.0fc\n", awayShort+":", awayYes, awayNo)
	}
	if fs.HomeTicker != "" || fs.AwayTicker != "" {
		fmt.Fprintf(&b, "    %-38s%s: %.0fc  |  %s %.0fc\n",
			"Best odds:", homeShort, bestHome, awayShort, bestAway)
	}
	if fs.ModelHomePct > 0 || fs.ModelAwayPct > 0 {
		fmt.Fprintf(&b, "    %-38s%s %.1f%%  |  %s %.1f%%\n",
			"Model:", homeShort, fs.ModelHomePct, awayShort, fs.ModelAwayPct)
	} else {
		fmt.Fprintf(&b, "    %-38s%s\n", "Model:", "(not computed)")
	}
	fmt.Fprintf(&b, "%s\n", divider)

	fmt.Fprint(os.Stderr, b.String())
}
