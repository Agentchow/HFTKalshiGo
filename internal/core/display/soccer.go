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

	homeShort := shortName(ss.HomeTeam)
	awayShort := shortName(ss.AwayTeam)

	wsTag := ""
	if !gc.KalshiConnected && len(gc.Tickers) > 0 {
		wsTag = "  [WS DOWN]"
	}

	titleEvent := eventType
	if eventType == "SCORE CHANGE" {
		switch gc.LastScorer {
		case "home":
			titleEvent = fmt.Sprintf("%s (%s goal)", eventType, homeShort)
		case "away":
			titleEvent = fmt.Sprintf("%s (%s goal)", eventType, awayShort)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n[%s %s]%s\n", titleEvent, ts, wsTag)
	if gc.KalshiEventURL != "" {
		fmt.Fprintf(&b, "%s\n", gc.KalshiEventURL)
	}
	fmt.Fprintf(&b, "%s\n", divider)
	fmt.Fprintf(&b, "  %s vs %s\n", ss.HomeTeam, ss.AwayTeam)
	fmt.Fprintf(&b, "    %-38s%s %.0f%%  |  Tie %.0f%%  |  %s %.0f%%  |  G0=%.2f\n",
		"Pregame Strength:", homeShort, ss.HomeStrength*100, ss.DrawPct*100, awayShort, ss.AwayStrength*100, ss.G0)
	scoreLine := fmt.Sprintf("Score %d-%d  |  %s (%s left)", ss.HomeScore, ss.AwayScore, ss.Half, fmtTimeLeft(ss.TimeLeft))
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

	fmt.Fprintf(&b, "\n")
	if hasKalshi {
		fmt.Fprintf(&b, "    %-40s%7.0fc%13.0fc%13.0fc\n", "Kalshi NO:", homeNo, drawNo, awayNo)
		// Best (lowest) cost to bet on each outcome:
		// Home: min(Home Yes, Away No + Draw No), TIE: min(Draw Yes, Home No + Away No), Away: min(Away Yes, Home No + Draw No)
		bestHome := homeYes
		if awayNo > 0 && drawNo > 0 && (bestHome == 0 || awayNo+drawNo < bestHome) {
			bestHome = awayNo + drawNo
		}
		bestDraw := drawYes
		if homeNo > 0 && awayNo > 0 && (bestDraw == 0 || homeNo+awayNo < bestDraw) {
			bestDraw = homeNo + awayNo
		}
		bestAway := awayYes
		if homeNo > 0 && drawNo > 0 && (bestAway == 0 || homeNo+drawNo < bestAway) {
			bestAway = homeNo + drawNo
		}
		fmt.Fprintf(&b, "    %-40s%7.0fc%13.0fc%13.0fc\n", "Best odds:", bestHome, bestDraw, bestAway)
	} else {
		fmt.Fprintf(&b, "    %-40s%8s%14s%14s\n", "Kalshi NO:", "—", "—", "—")
	}

	fmt.Fprintf(&b, "%s\n", divider)

	fmt.Fprint(os.Stderr, b.String())
}
