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
	if eventType == "EDGE" {
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

	// Bet365
	b365Home := pctOrZero(ss.Bet365HomePct)
	b365Draw := pctOrZero(ss.Bet365DrawPct)
	b365Away := pctOrZero(ss.Bet365AwayPct)
	hasBet365 := ss.Bet365HomePct != nil && ss.Bet365DrawPct != nil && ss.Bet365AwayPct != nil

	edgeHomeYes := ss.EdgeHomeYes
	edgeDrawYes := ss.EdgeDrawYes
	edgeAwayYes := ss.EdgeAwayYes
	edgeHomeNo := ss.EdgeHomeNo
	edgeDrawNo := ss.EdgeDrawNo
	edgeAwayNo := ss.EdgeAwayNo

	homeShort := shortName(ss.HomeTeam)
	awayShort := shortName(ss.AwayTeam)

	wsTag := ""
	if !gc.KalshiConnected && len(gc.Tickers) > 0 {
		wsTag = "  [WS DOWN]"
	}

	var b strings.Builder
	if gc.KalshiEventURL != "" {
		fmt.Fprintf(&b, "\n[%s %s]  %s%s\n", eventType, ts, gc.KalshiEventURL, wsTag)
	} else {
		fmt.Fprintf(&b, "\n[%s %s]%s\n", eventType, ts, wsTag)
	}
	fmt.Fprintf(&b, "%s\n", divider)
	fmt.Fprintf(&b, "  %s vs %s\n", ss.HomeTeam, ss.AwayTeam)
	fmt.Fprintf(&b, "    %-38s%s %.0f%%  |  Tie %.0f%%  |  %s %.0f%%  |  G0=%.2f\n",
		"Pregame Strength:", homeShort, ss.HomeStrength*100, ss.DrawPct*100, awayShort, ss.AwayStrength*100, ss.G0)
	scoreLine := fmt.Sprintf("Score %d-%d  |  %s (~%s left)", ss.HomeScore, ss.AwayScore, ss.Half, fmtTimeLeft(ss.TimeLeft))
	if ss.HomeRedCards > 0 || ss.AwayRedCards > 0 {
		scoreLine += fmt.Sprintf("  |  Red Cards: H=%d A=%d", ss.HomeRedCards, ss.AwayRedCards)
	}
	fmt.Fprintf(&b, "    %-38s%s\n", "Score & time:", scoreLine)

	hasKalshi := homeYes > 0 || homeNo > 0 || drawYes > 0 || drawNo > 0 || awayYes > 0 || awayNo > 0

	// 3-column header — widths 8/14/14; data uses %7.0fc so "Nc" fits in 8, etc.
	fmt.Fprintf(&b, "    %40s%8s%14s%14s\n", "", homeShort, "TIE", awayShort)
	if !gc.KalshiConnected && len(gc.Tickers) > 0 {
		fmt.Fprintf(&b, "    *** Kalshi WS disconnected — prices stale ***\n")
	}
	if hasKalshi {
		fmt.Fprintf(&b, "    %-40s%7.0fc%13.0fc%13.0fc\n", "Kalshi YES:", homeYes, drawYes, awayYes)
	} else {
		fmt.Fprintf(&b, "    %-40s%8s%14s%14s\n", "Kalshi YES:", "—", "—", "—")
	}
	if hasBet365 {
		fmt.Fprintf(&b, "    %-40s%7.1f%%%13.1f%%%13.1f%%\n", "Bet365 YES:", b365Home, b365Draw, b365Away)
		if hasKalshi {
			fmt.Fprintf(&b, "    %-40s%8s%14s%14s\n", "Edge YES:",
				fmtEdge(edgeHomeYes), fmtEdge(edgeDrawYes), fmtEdge(edgeAwayYes))
		}
	} else {
		fmt.Fprintf(&b, "    %-40s%s\n", "Bet365 YES:", "(not available)")
	}

	fmt.Fprintf(&b, "\n")
	if hasKalshi {
		fmt.Fprintf(&b, "    %-40s%7.0fc%13.0fc%13.0fc\n", "Kalshi NO:", homeNo, drawNo, awayNo)
	} else {
		fmt.Fprintf(&b, "    %-40s%8s%14s%14s\n", "Kalshi NO:", "—", "—", "—")
	}
	if hasBet365 {
		fmt.Fprintf(&b, "    %-40s%7.1f%%%13.1f%%%13.1f%%\n", "Bet365 NO:", 100-b365Home, 100-b365Draw, 100-b365Away)
		if hasKalshi {
			fmt.Fprintf(&b, "    %-40s%8s%14s%14s\n", "Edge NO:",
				fmtEdge(edgeHomeNo), fmtEdge(edgeDrawNo), fmtEdge(edgeAwayNo))
		}
	}

	// Edge summary line — only when both sources are available; Tie in the middle
	if hasBet365 && hasKalshi {
		var homeEdges, tieEdges, awayEdges []string
		if edgeHomeYes >= 3.0 {
			homeEdges = append(homeEdges, fmt.Sprintf("%s YES %+.1f%%", homeShort, edgeHomeYes))
		}
		if edgeHomeNo >= 3.0 {
			homeEdges = append(homeEdges, fmt.Sprintf("%s NO %+.1f%%", homeShort, edgeHomeNo))
		}
		if edgeDrawYes >= 3.0 {
			tieEdges = append(tieEdges, fmt.Sprintf("Tie YES %+.1f%%", edgeDrawYes))
		}
		if edgeDrawNo >= 3.0 {
			tieEdges = append(tieEdges, fmt.Sprintf("Tie NO %+.1f%%", edgeDrawNo))
		}
		if edgeAwayYes >= 3.0 {
			awayEdges = append(awayEdges, fmt.Sprintf("%s YES %+.1f%%", awayShort, edgeAwayYes))
		}
		if edgeAwayNo >= 3.0 {
			awayEdges = append(awayEdges, fmt.Sprintf("%s NO %+.1f%%", awayShort, edgeAwayNo))
		}
		all := append(append(homeEdges, tieEdges...), awayEdges...)
		if len(all) > 0 {
			fmt.Fprintf(&b, "    >>> %s\n", strings.Join(all, " | "))
		}
	}

	fmt.Fprintf(&b, "%s\n", divider)

	fmt.Fprint(os.Stderr, b.String())
}

func fmtEdge(e float64) string {
	return fmt.Sprintf("%+.1f%%", e)
}
