package display

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
)

func PrintSoccer(gc *game.GameContext, eventType string) {
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return
	}

	divider := dividerHeavy
	if eventType == "TICKER UPDATE" {
		divider = dividerLight
	}

	ts := time.Now().Format("3:04:05.000 PM")

	// Kalshi prices
	var homeYes, homeNo, drawYes, drawNo, awayYes, awayNo float64
	if td, ok := gc.Tickers[ss.HomeTicker]; ok {
		homeYes = td.YesAsk
		homeNo = td.NoAsk
	}
	if td, ok := gc.Tickers[ss.DrawTicker]; ok {
		drawYes = td.YesAsk
		drawNo = td.NoAsk
	}
	if td, ok := gc.Tickers[ss.AwayTicker]; ok {
		awayYes = td.YesAsk
		awayNo = td.NoAsk
	}

	// Pinnacle
	pinnHome := pctOrZero(ss.PinnacleHomePct)
	pinnDraw := pctOrZero(ss.PinnacleDrawPct)
	pinnAway := pctOrZero(ss.PinnacleAwayPct)
	hasPinnacle := ss.PinnacleHomePct != nil && ss.PinnacleDrawPct != nil && ss.PinnacleAwayPct != nil

	// Edge = Pinnacle - Kalshi (percentage points)
	edgeHomeYes := pinnHome - homeYes
	edgeDrawYes := pinnDraw - drawYes
	edgeAwayYes := pinnAway - awayYes
	edgeHomeNo := (100 - pinnHome) - homeNo
	edgeDrawNo := (100 - pinnDraw) - drawNo
	edgeAwayNo := (100 - pinnAway) - awayNo

	homeShort := shortName(ss.HomeTeam)
	awayShort := shortName(ss.AwayTeam)

	var b strings.Builder
	fmt.Fprintf(&b, "\n[%s %s]\n", eventType, ts)
	fmt.Fprintf(&b, "%s\n", divider)
	fmt.Fprintf(&b, "  %s @ %s  (Home: %s)\n", ss.AwayTeam, ss.HomeTeam, homeShort)
	fmt.Fprintf(&b, "    %-38sHome %.0f%%  |  Draw %.0f%%  |  Away %.0f%%  |  G0=%.2f\n",
		"Pregame:", ss.HomeWinPct*100, ss.DrawPct*100, ss.AwayWinPct*100, ss.G0)
	fmt.Fprintf(&b, "    %-38sScore %d-%d  |  %s (~%.0f min left)\n",
		"Score & time:", ss.HomeScore, ss.AwayScore, ss.Half, ss.TimeLeft)

	// 3-column header
	fmt.Fprintf(&b, "    %40s%s%12s%12s\n", "", "HOME", "DRAW", "AWAY")
	fmt.Fprintf(&b, "    %-40s%4.0fc%12.0fc%12.0fc\n", "Kalshi YES:", homeYes, drawYes, awayYes)
	if hasPinnacle {
		fmt.Fprintf(&b, "    %-40s%3.1f%%%11.1f%%%11.1f%%\n", "Pinnacle YES:", pinnHome, pinnDraw, pinnAway)
		fmt.Fprintf(&b, "    %-40s%s%12s%12s\n", "Edge YES:",
			fmtEdge(edgeHomeYes), fmtEdge(edgeDrawYes), fmtEdge(edgeAwayYes))
	} else {
		fmt.Fprintf(&b, "    %-40s%s\n", "Pinnacle YES:", "(not available)")
	}

	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "    %-40s%4.0fc%12.0fc%12.0fc\n", "Kalshi NO:", homeNo, drawNo, awayNo)
	if hasPinnacle {
		fmt.Fprintf(&b, "    %-40s%3.1f%%%11.1f%%%11.1f%%\n", "Pinnacle NO:", 100-pinnHome, 100-pinnDraw, 100-pinnAway)
		fmt.Fprintf(&b, "    %-40s%s%12s%12s\n", "Edge NO:",
			fmtEdge(edgeHomeNo), fmtEdge(edgeDrawNo), fmtEdge(edgeAwayNo))
	}

	// Edge summary line
	if hasPinnacle {
		var edges []string
		for _, e := range []struct {
			name string
			side string
			val  float64
		}{
			{homeShort, "YES", edgeHomeYes},
			{awayShort, "YES", edgeAwayYes},
			{"Draw", "YES", edgeDrawYes},
			{homeShort, "NO", edgeHomeNo},
			{awayShort, "NO", edgeAwayNo},
			{"Draw", "NO", edgeDrawNo},
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

func fmtEdge(e float64) string {
	return fmt.Sprintf("%+.1f%%", e)
}
