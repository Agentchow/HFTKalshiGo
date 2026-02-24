// goalserve_mock sends fake GoalServe webhook payloads to the local webhook
// server (localhost:8765) to exercise the full pipeline end-to-end.
//
// On startup it queries the Kalshi API for active soccer, hockey, and football
// markets, picks one game from each sport, and uses those team names in the
// mock webhooks. Each sport runs on its own goroutine.
// No odds are included in the payloads so Pinnacle stays nil,
// RecalcEdge is a no-op, and no orders are placed.
//
// Each run uses a unique EID (timestamp-based) so the engine creates a fresh
// GameContext.
//
// Usage:
//
//	go run cmd/goalserve_mock/main.go
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/config"
)

const target = "http://localhost:8765"

var seriesToLeague = map[string]string{
	"KXEPLGAME":             "English Premier League",
	"KXUCLGAME":             "UEFA Champions League",
	"KXLALIGAGAME":          "La Liga",
	"KXBUNDESLIGAGAME":      "Bundesliga",
	"KXSERIEAGAME":          "Serie A",
	"KXLIGUE1GAME":          "Ligue 1",
	"KXLIGAMXGAME":          "Liga MX",
	"KXALEAGUEGAME":         "A-League",
	"KXJLEAGUEGAME":         "J-League",
	"KXDIMAYORGAME":         "Dimayor",
	"KXAFCCLGAME":           "AFC Champions League",
	"KXSAUDIPLGAME":         "Saudi Pro League",
	"KXUELGAME":             "UEFA Europa League",
	"KXUECLGAME":            "UEFA Europa Conference League",
	"KXARGPREMDIVGAME":      "Argentina Primera Division",
	"KXBRASILEIROGAME":      "Brasileirao",
	"KXSUPERLIGGAME":        "Super Lig",
	"KXEKSTRAKLASAGAME":     "Ekstraklasa",
	"KXHNLGAME":             "HNL",
	"KXBUNDESLIGA2GAME":     "2. Bundesliga",
	"KXLALIGA2GAME":         "La Liga 2",
	"KXEREDIVISIEGAME":      "Eredivisie",
	"KXSERIEBGAME":          "Serie B",
	"KXBELGIANPLGAME":       "Belgian Pro League",
	"KXEFLCHAMPIONSHIPGAME": "EFL Championship",
	"KXLIGAPORTUGALGAME":    "Liga Portugal",
	"KXDENSUPERLIGAGAME":    "Danish Superliga",
	"KXMLSGAME":             "MLS",
	"KXNHLGAME":             "NHL",
	"KXAHLGAME":             "AHL",
	"KXKHLGAME":             "KHL",
	"KXSHLGAME":             "SHL",
	"KXLIIGAGAME":           "Liiga",
	"KXELHGAME":             "ELH",
	"KXNFLGAME":             "NFL",
	"KXNCAAFGAME":           "NCAAF",
}

type gameInfo struct {
	homeTeam string
	awayTeam string
	league   string
}

func main() {
	fmt.Println("=== GoalServe Mock ===")
	fmt.Println("Fetching active Kalshi markets...\n")

	soccerGame, hockeyGame, footballGame := discoverGames()

	var wg sync.WaitGroup

	if soccerGame != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runSoccerMocks(soccerGame)
		}()
	} else {
		fmt.Println("── No active soccer games found on Kalshi, skipping ──")
	}

	if hockeyGame != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runHockeyMocks(hockeyGame)
		}()
	} else {
		fmt.Println("── No active hockey games found on Kalshi, skipping ──")
	}

	if footballGame != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runFootballMocks(footballGame)
		}()
	} else {
		fmt.Println("── No active football games found on Kalshi, skipping ──")
	}

	wg.Wait()
	fmt.Println("\nDone!")
}

func runSoccerMocks(g *gameInfo) {
	fmt.Printf("── Soccer Mock 1: %s vs %s (%s) ──\n", g.homeTeam, g.awayTeam, g.league)
	fmt.Println("  Sequence: 0-0 → 1-0 → RC → 2-0 → [false drop 1-0, rejected] → 2-1 → Finished 2-1\n")

	eid := fmt.Sprintf("MOCK-SOC-%d", time.Now().Unix())
	runSoccerGame(eid, g.homeTeam, g.awayTeam, g.league,
		[]frame{
			{home: 0, away: 0, period: "1st Half", minute: "1", redH: 0, redA: 0, label: "Game Start (0-0, 1st min)"},
			{home: 0, away: 0, period: "1st Half", minute: "1", redH: 0, redA: 0, label: "  (warm-up)"},
			{home: 0, away: 0, period: "1st Half", minute: "1", redH: 0, redA: 0, label: "  (warm-up)"},
			{home: 1, away: 0, period: "1st Half", minute: "23", redH: 0, redA: 0, label: "GOAL! 1-0 (23rd min)"},
			{home: 1, away: 0, period: "1st Half", minute: "35", redH: 0, redA: 1, label: "RED CARD away (35th min)"},
			{home: 2, away: 0, period: "2nd Half", minute: "58", redH: 0, redA: 1, label: "GOAL! 2-0 (58th min)"},
			{home: 1, away: 0, period: "2nd Half", minute: "59", redH: 0, redA: 1, label: "FALSE DROP 2-0 → 1-0 (pending)"},
			{home: 1, away: 0, period: "2nd Half", minute: "59", redH: 0, redA: 1, label: "  overturn pending (4s)"},
			{home: 1, away: 0, period: "2nd Half", minute: "60", redH: 0, redA: 1, label: "  overturn pending (6s)"},
			{home: 1, away: 0, period: "2nd Half", minute: "60", redH: 0, redA: 1, label: "  overturn pending (8s)"},
			{home: 2, away: 0, period: "2nd Half", minute: "61", redH: 0, redA: 1, label: "RESTORED 2-0 (overturn rejected)"},
			{home: 2, away: 1, period: "2nd Half", minute: "72", redH: 0, redA: 1, label: "GOAL! 2-1 (72nd min)"},
			{home: 2, away: 1, period: "2nd Half", minute: "72", redH: 0, redA: 1, label: "  (no change)"},
			{home: 2, away: 1, period: "Finished", minute: "90", redH: 0, redA: 1, label: "FULL TIME 2-1"},
		},
	)

	fmt.Printf("\n── Soccer Mock 2: %s vs %s (%s) ──\n", g.homeTeam, g.awayTeam, g.league)
	fmt.Println("  Sequence: 0-0 → 1-0 → 2-0 → [2-1 overturned → back to 2-0] → 2-1 → Finished 2-1\n")

	eid2 := fmt.Sprintf("MOCK-SOC2-%d", time.Now().Unix())
	runSoccerOverturnGame(eid2, g.homeTeam, g.awayTeam, g.league)
}

func runHockeyMocks(g *gameInfo) {
	fmt.Printf("── Hockey Mock 1: %s vs %s (%s) ──\n", g.homeTeam, g.awayTeam, g.league)
	fmt.Println("  OT game with false alarm: 0-0 → 1-0 → 1-1 → PPG 2-1 → [false drop 1-1, rejected] → 2-2 → OT 3-2\n")

	hEid := fmt.Sprintf("MOCK-HOC-%d", time.Now().Unix())
	runHockeyGame(hEid, g.homeTeam, g.awayTeam, g.league,
		[]hockeyFrame{
			{home: 0, away: 0, period: "1st Period", seconds: "19:30", sts: "", label: "Game Start (0-0)"},
			{home: 0, away: 0, period: "1st Period", seconds: "19:28", sts: "", label: "  (warm-up)"},
			{home: 0, away: 0, period: "1st Period", seconds: "19:26", sts: "", label: "  (warm-up)"},
			{home: 1, away: 0, period: "1st Period", seconds: "12:45", sts: "", label: "GOAL! 1-0"},
			{home: 1, away: 0, period: "1st Period", seconds: "8:30", sts: "Penalties=1:0|Goals on Power Play=0:0|INFO=5 ON 4|", label: "POWER PLAY #1 (home PP)"},
			{home: 1, away: 0, period: "1st Period", seconds: "6:30", sts: "Penalties=1:0|Goals on Power Play=0:0|INFO=|", label: "PP #1 ends"},
			{home: 1, away: 1, period: "2nd Period", seconds: "14:20", sts: "Penalties=1:0|", label: "GOAL! 1-1"},
			{home: 1, away: 1, period: "2nd Period", seconds: "9:15", sts: "Penalties=1:1|Goals on Power Play=0:0|INFO=5 ON 4|", label: "POWER PLAY #2 (home PP)"},
			{home: 2, away: 1, period: "2nd Period", seconds: "8:02", sts: "Penalties=1:1|Goals on Power Play=1:0|INFO=5 ON 4|", label: "PPG! 2-1 (home scores on PP)"},
			{home: 2, away: 1, period: "2nd Period", seconds: "7:00", sts: "Penalties=1:1|Goals on Power Play=1:0|INFO=|", label: "PP #2 ends"},
			{home: 1, away: 1, period: "2nd Period", seconds: "6:50", sts: "", label: "FALSE DROP 2-1 → 1-1 (pending)"},
			{home: 1, away: 1, period: "2nd Period", seconds: "6:48", sts: "", label: "  overturn pending (4s)"},
			{home: 1, away: 1, period: "2nd Period", seconds: "6:46", sts: "", label: "  overturn pending (6s)"},
			{home: 1, away: 1, period: "2nd Period", seconds: "6:44", sts: "", label: "  overturn pending (8s)"},
			{home: 2, away: 1, period: "2nd Period", seconds: "6:42", sts: "", label: "RESTORED 2-1 (overturn rejected)"},
			{home: 2, away: 2, period: "3rd Period", seconds: "4:10", sts: "", label: "GOAL! 2-2"},
			{home: 2, away: 2, period: "3rd Period", seconds: "0:00", sts: "", label: "End of regulation"},
			{home: 2, away: 2, period: "Overtimer", seconds: "5:00", sts: "", label: "OVERTIME starts"},
			{home: 3, away: 2, period: "Overtimer", seconds: "2:33", sts: "", label: "OT GOAL! 3-2 home wins"},
			{home: 3, away: 2, period: "Finished", seconds: "", sts: "", label: "FINAL 3-2 OT"},
		},
	)

	fmt.Printf("\n── Hockey Mock 2: %s vs %s (%s) ──\n", g.homeTeam, g.awayTeam, g.league)
	fmt.Println("  Overturn game: 0-0 → 1-0 → 2-0 → [3-0 overturned → back to 2-0] → 3-0 → Finished 3-0\n")

	hEid2 := fmt.Sprintf("MOCK-HOC2-%d", time.Now().Unix())
	runHockeyOverturnGame(hEid2, g.homeTeam, g.awayTeam, g.league)
}

func runFootballMocks(g *gameInfo) {
	fmt.Printf("── Football Mock 1: %s vs %s (%s) ──\n", g.homeTeam, g.awayTeam, g.league)
	fmt.Println("  Sequence: 0-0 → FG 3-0 → TD 10-0 → FG 10-3 → TD 10-10 → [false drop 3-10, rejected] → FG 13-10 → TD 20-10 → Finished 20-10")

	eid := fmt.Sprintf("MOCK-FB-%d", time.Now().Unix())
	runFootballGame(eid, g.homeTeam, g.awayTeam, g.league,
		[]footballFrame{
			{home: 0, away: 0, period: "1st Quarter", seconds: "15:00", label: "Game Start (0-0)"},
			{home: 0, away: 0, period: "1st Quarter", seconds: "14:55", label: "  (warm-up)"},
			{home: 0, away: 0, period: "1st Quarter", seconds: "14:50", label: "  (warm-up)"},
			{home: 3, away: 0, period: "1st Quarter", seconds: "8:22", label: "FG! 3-0"},
			{home: 10, away: 0, period: "2nd Quarter", seconds: "11:45", label: "TD+XP! 10-0"},
			{home: 10, away: 3, period: "2nd Quarter", seconds: "4:30", label: "FG! 10-3"},
			{home: 10, away: 10, period: "3rd Quarter", seconds: "6:15", label: "TD+XP! 10-10"},
			{home: 3, away: 10, period: "3rd Quarter", seconds: "5:50", label: "FALSE DROP 10-10 → 3-10 (pending)"},
			{home: 3, away: 10, period: "3rd Quarter", seconds: "5:48", label: "  overturn pending (4s)"},
			{home: 3, away: 10, period: "3rd Quarter", seconds: "5:46", label: "  overturn pending (6s)"},
			{home: 3, away: 10, period: "3rd Quarter", seconds: "5:44", label: "  overturn pending (8s)"},
			{home: 10, away: 10, period: "3rd Quarter", seconds: "5:42", label: "RESTORED 10-10 (overturn rejected)"},
			{home: 13, away: 10, period: "4th Quarter", seconds: "9:10", label: "FG! 13-10"},
			{home: 20, away: 10, period: "4th Quarter", seconds: "2:05", label: "TD+XP! 20-10"},
			{home: 20, away: 10, period: "4th Quarter", seconds: "0:00", label: "End of regulation"},
			{home: 20, away: 10, period: "Finished", seconds: "", label: "FINAL 20-10"},
		},
	)

	fmt.Printf("\n── Football Mock 2: %s vs %s (%s) ──\n", g.homeTeam, g.awayTeam, g.league)
	fmt.Println("  Overturn game: 0-0 → TD 7-0 → TD 14-0 → [TD 21-0 overturned → back to 14-0] → TD 21-0 → Finished 21-0")

	eid2 := fmt.Sprintf("MOCK-FB2-%d", time.Now().Unix())
	runFootballOverturnGame(eid2, g.homeTeam, g.awayTeam, g.league)
}

// discoverGames queries the Kalshi API and returns the first active soccer,
// hockey, and football game found. Returns nil for a sport if no markets are open.
func discoverGames() (soccer, hockey, football *gameInfo) {
	cfg := config.Load()
	signer, err := kalshi_auth.NewSignerFromFile(cfg.KalshiKeyID, cfg.KalshiKeyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kalshi auth error: %v\n", err)
		return nil, nil, nil
	}
	client := kalshi_http.NewClient(cfg.KalshiBaseURL, signer, cfg.RateDivisor)
	ctx := context.Background()

	soccerSeries := []string{
		"KXEPLGAME", "KXUCLGAME", "KXLALIGAGAME", "KXBUNDESLIGAGAME",
		"KXSERIEAGAME", "KXLIGUE1GAME", "KXLIGAMXGAME", "KXMLSGAME",
		"KXUELGAME", "KXUECLGAME", "KXEFLCHAMPIONSHIPGAME",
		"KXSAUDIPLGAME", "KXLIGAPORTUGALGAME", "KXEREDIVISIEGAME",
		"KXBELGIANPLGAME", "KXSERIEBGAME", "KXDIMAYORGAME",
	}
	hockeySeries := []string{
		"KXNHLGAME", "KXAHLGAME", "KXKHLGAME", "KXSHLGAME",
	}
	footballSeries := []string{
		"KXNFLGAME", "KXNCAAFGAME",
	}

	soccer = findGame(ctx, client, soccerSeries, true)
	hockey = findGame(ctx, client, hockeySeries, false)
	football = findGame(ctx, client, footballSeries, false)
	return
}

func findGame(ctx context.Context, client *kalshi_http.Client, seriesList []string, isSoccer bool) *gameInfo {
	now := time.Now()
	cutoff := now.Add(24 * time.Hour)

	for _, series := range seriesList {
		markets, err := client.GetMarkets(ctx, series)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: fetch %s failed: %v\n", series, err)
			continue
		}
		if len(markets) == 0 {
			continue
		}

		byEvent := make(map[string][]kalshi_http.Market)
		for _, m := range markets {
			if m.EventTicker != "" {
				byEvent[m.EventTicker] = append(byEvent[m.EventTicker], m)
			}
		}

		for eventTicker, group := range byEvent {
			expiry := latestExpiry(group)
			if expiry.IsZero() || expiry.After(cutoff) {
				continue
			}

			home, away := extractTeamNames(group, isSoccer)
			if home == "" || away == "" {
				continue
			}
			league := seriesToLeague[strings.ToUpper(series)]
			if league == "" {
				league = series
			}
			fmt.Printf("  Found: %s vs %s (%s) [%s] expires %s\n",
				home, away, league, eventTicker, expiry.Format("Jan 02 15:04 MST"))
			return &gameInfo{homeTeam: home, awayTeam: away, league: league}
		}
	}
	return nil
}

func latestExpiry(markets []kalshi_http.Market) time.Time {
	var latest time.Time
	for _, m := range markets {
		t := parseExpiry(m.ExpectedExpirationTime)
		if t.IsZero() {
			t = parseExpiry(m.CloseTime)
		}
		if !t.IsZero() && t.After(latest) {
			latest = t
		}
	}
	return latest
}

func parseExpiry(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func extractTeamNames(group []kalshi_http.Market, isSoccer bool) (home, away string) {
	var teamMarkets []kalshi_http.Market
	for _, m := range group {
		if isSoccer && strings.HasSuffix(strings.ToUpper(m.Ticker), "-TIE") {
			continue
		}
		teamMarkets = append(teamMarkets, m)
	}

	if isSoccer && len(teamMarkets) != 2 {
		return "", ""
	}
	if !isSoccer && len(teamMarkets) != 2 {
		return "", ""
	}

	home = cleanTeamName(teamMarkets[0].YesSubTitle)
	away = cleanTeamName(teamMarkets[1].YesSubTitle)

	if home == "" || away == "" {
		home, away = parseTitle(teamMarkets[0].Title)
	}
	return
}

func cleanTeamName(subtitle string) string {
	s := strings.TrimSpace(subtitle)
	for _, suffix := range []string{" to Win", " Winner", " Wins", " Win"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

func parseTitle(title string) (string, string) {
	for _, sep := range []string{" at ", " vs. ", " vs "} {
		if idx := strings.Index(title, sep); idx >= 0 {
			t1 := strings.TrimSpace(title[:idx])
			rest := strings.TrimSpace(title[idx+len(sep):])
			rest = strings.TrimSuffix(rest, " Winner?")
			rest = strings.TrimSuffix(rest, " Winner")
			rest = strings.TrimSuffix(rest, "?")
			return strings.TrimSpace(t1), strings.TrimSpace(rest)
		}
	}
	return "", ""
}

// ── Frame types ────────────────────────────────────────────────────────

type frame struct {
	home, away     int
	period, minute string
	redH, redA     int
	label          string
}

type hockeyFrame struct {
	home, away      int
	period, seconds string
	sts             string
	label           string
}

type footballFrame struct {
	home, away      int
	period, seconds string
	label           string
}

// ── Senders ────────────────────────────────────────────────────────────

func runSoccerGame(eid, homeTeam, awayTeam, league string, frames []frame) {
	for i, f := range frames {
		ev := map[string]any{
			"info": map[string]any{
				"name":         fmt.Sprintf("%s vs %s", homeTeam, awayTeam),
				"period":       f.period,
				"status":       f.period,
				"minute":       f.minute,
				"league":       league,
				"category":     "soccer",
				"start_ts_utc": fmt.Sprintf("%d", time.Now().Add(-30*time.Minute).Unix()),
			},
			"team_info": map[string]any{
				"home": map[string]string{"name": homeTeam, "score": fmt.Sprintf("%d", f.home)},
				"away": map[string]string{"name": awayTeam, "score": fmt.Sprintf("%d", f.away)},
			},
			"stats": map[string]any{
				"redcards_home": f.redH,
				"redcards_away": f.redA,
			},
		}

		payload := map[string]any{
			"updated":    time.Now().Format(time.RFC3339),
			"updated_ts": time.Now().Unix(),
			"events":     map[string]any{eid: ev},
		}

		send("/webhook/soccer", payload, i+1, len(frames), f.label)
		time.Sleep(2 * time.Second)
	}
}

func runHockeyGame(eid, homeTeam, awayTeam, league string, frames []hockeyFrame) {
	for i, f := range frames {
		ev := map[string]any{
			"info": map[string]any{
				"name":         fmt.Sprintf("%s vs %s", homeTeam, awayTeam),
				"period":       f.period,
				"status":       f.period,
				"seconds":      f.seconds,
				"league":       league,
				"category":     "hockey",
				"start_ts_utc": fmt.Sprintf("%d", time.Now().Add(-90*time.Minute).Unix()),
			},
			"team_info": map[string]any{
				"home": map[string]string{"name": homeTeam, "score": fmt.Sprintf("%d", f.home)},
				"away": map[string]string{"name": awayTeam, "score": fmt.Sprintf("%d", f.away)},
			},
			"sts": f.sts,
		}

		payload := map[string]any{
			"updated":    time.Now().Format(time.RFC3339),
			"updated_ts": time.Now().Unix(),
			"events":     map[string]any{eid: ev},
		}

		send("/webhook/hockey", payload, i+1, len(frames), f.label)
		time.Sleep(2 * time.Second)
	}
}

func runHockeyOverturnGame(eid, homeTeam, awayTeam, league string) {
	normal := []hockeyFrame{
		{home: 0, away: 0, period: "1st Period", seconds: "19:30", sts: "", label: "Game Start (0-0)"},
		{home: 0, away: 0, period: "1st Period", seconds: "19:28", sts: "", label: "  (warm-up)"},
		{home: 1, away: 0, period: "1st Period", seconds: "14:10", sts: "", label: "GOAL! 1-0"},
		{home: 2, away: 0, period: "2nd Period", seconds: "11:30", sts: "", label: "GOAL! 2-0"},
		{home: 3, away: 0, period: "2nd Period", seconds: "6:45", sts: "", label: "GOAL! 3-0 (will be overturned)"},
	}

	confirmFrames := 9 // 9 frames × 2s = 18s > 15s SCORE_DROP_CONFIRM_SEC
	totalSteps := len(normal) + 1 + confirmFrames + 3
	step := 0

	for _, f := range normal {
		step++
		sendHockeyFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}

	// Score drops from 3-0 back to 2-0 — send repeatedly for 18+ seconds
	// to exceed the 15s SCORE_DROP_CONFIRM_SEC window.
	step++
	dropFrame := hockeyFrame{home: 2, away: 0, period: "2nd Period", seconds: "6:40", sts: "", label: "OVERTURN! 3-0 → 2-0 (pending)"}
	sendHockeyFrame(eid, homeTeam, awayTeam, league, dropFrame, step, totalSteps)
	time.Sleep(2 * time.Second)

	for i := 0; i < confirmFrames; i++ {
		step++
		secs := fmt.Sprintf("6:%02d", 38-i)
		f := hockeyFrame{home: 2, away: 0, period: "2nd Period", seconds: secs, sts: "", label: fmt.Sprintf("  confirming overturn (%ds)", (i+2)*2)}
		sendHockeyFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}

	// Overturn confirmed by now (20s > 15s). Score another real goal, then finish.
	post := []hockeyFrame{
		{home: 3, away: 0, period: "3rd Period", seconds: "8:20", sts: "", label: "GOAL! 3-0 (real this time)"},
		{home: 3, away: 0, period: "3rd Period", seconds: "0:00", sts: "", label: "End of regulation"},
		{home: 3, away: 0, period: "Finished", seconds: "", sts: "", label: "FINAL 3-0 (no OT)"},
	}
	for _, f := range post {
		step++
		sendHockeyFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}
}

func runSoccerOverturnGame(eid, homeTeam, awayTeam, league string) {
	normal := []frame{
		{home: 0, away: 0, period: "1st Half", minute: "1", redH: 0, redA: 0, label: "Game Start (0-0, 1st min)"},
		{home: 0, away: 0, period: "1st Half", minute: "1", redH: 0, redA: 0, label: "  (warm-up)"},
		{home: 1, away: 0, period: "1st Half", minute: "18", redH: 0, redA: 0, label: "GOAL! 1-0 (18th min)"},
		{home: 2, away: 0, period: "2nd Half", minute: "42", redH: 0, redA: 0, label: "GOAL! 2-0 (42nd min)"},
		{home: 2, away: 1, period: "2nd Half", minute: "55", redH: 0, redA: 0, label: "GOAL! 2-1 (55th min, will be overturned)"},
	}

	confirmFrames := 9 // 9 frames × 2s = 18s > 15s SCORE_DROP_CONFIRM_SEC
	totalSteps := len(normal) + 1 + confirmFrames + 3
	step := 0

	for _, f := range normal {
		step++
		sendSoccerFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}

	// Score drops from 2-1 back to 2-0 — send repeatedly for 18+ seconds
	// to exceed the 15s SCORE_DROP_CONFIRM_SEC window.
	step++
	dropFrame := frame{home: 2, away: 0, period: "2nd Half", minute: "56", redH: 0, redA: 0, label: "OVERTURN! 2-1 → 2-0 (pending)"}
	sendSoccerFrame(eid, homeTeam, awayTeam, league, dropFrame, step, totalSteps)
	time.Sleep(2 * time.Second)

	for i := 0; i < confirmFrames; i++ {
		step++
		f := frame{home: 2, away: 0, period: "2nd Half", minute: fmt.Sprintf("%d", 56+i/3), redH: 0, redA: 0, label: fmt.Sprintf("  confirming overturn (%ds)", (i+2)*2)}
		sendSoccerFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}

	// Overturn confirmed (20s > 15s). Score a real goal, then finish.
	post := []frame{
		{home: 2, away: 1, period: "2nd Half", minute: "70", redH: 0, redA: 0, label: "GOAL! 2-1 (70th min, real this time)"},
		{home: 2, away: 1, period: "2nd Half", minute: "85", redH: 0, redA: 0, label: "  (no change)"},
		{home: 2, away: 1, period: "Finished", minute: "90", redH: 0, redA: 0, label: "FULL TIME 2-1"},
	}
	for _, f := range post {
		step++
		sendSoccerFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}
}

func sendSoccerFrame(eid, homeTeam, awayTeam, league string, f frame, step, total int) {
	ev := map[string]any{
		"info": map[string]any{
			"name":         fmt.Sprintf("%s vs %s", homeTeam, awayTeam),
			"period":       f.period,
			"status":       f.period,
			"minute":       f.minute,
			"league":       league,
			"category":     "soccer",
			"start_ts_utc": fmt.Sprintf("%d", time.Now().Add(-30*time.Minute).Unix()),
		},
		"team_info": map[string]any{
			"home": map[string]string{"name": homeTeam, "score": fmt.Sprintf("%d", f.home)},
			"away": map[string]string{"name": awayTeam, "score": fmt.Sprintf("%d", f.away)},
		},
		"stats": map[string]any{
			"redcards_home": f.redH,
			"redcards_away": f.redA,
		},
	}

	payload := map[string]any{
		"updated":    time.Now().Format(time.RFC3339),
		"updated_ts": time.Now().Unix(),
		"events":     map[string]any{eid: ev},
	}

	send("/webhook/soccer", payload, step, total, f.label)
}

func sendHockeyFrame(eid, homeTeam, awayTeam, league string, f hockeyFrame, step, total int) {
	ev := map[string]any{
		"info": map[string]any{
			"name":         fmt.Sprintf("%s vs %s", homeTeam, awayTeam),
			"period":       f.period,
			"status":       f.period,
			"seconds":      f.seconds,
			"league":       league,
			"category":     "hockey",
			"start_ts_utc": fmt.Sprintf("%d", time.Now().Add(-90*time.Minute).Unix()),
		},
		"team_info": map[string]any{
			"home": map[string]string{"name": homeTeam, "score": fmt.Sprintf("%d", f.home)},
			"away": map[string]string{"name": awayTeam, "score": fmt.Sprintf("%d", f.away)},
		},
		"sts": f.sts,
	}

	payload := map[string]any{
		"updated":    time.Now().Format(time.RFC3339),
		"updated_ts": time.Now().Unix(),
		"events":     map[string]any{eid: ev},
	}

	send("/webhook/hockey", payload, step, total, f.label)
}

func runFootballGame(eid, homeTeam, awayTeam, league string, frames []footballFrame) {
	for i, f := range frames {
		sendFootballFrame(eid, homeTeam, awayTeam, league, f, i+1, len(frames))
		time.Sleep(2 * time.Second)
	}
}

func runFootballOverturnGame(eid, homeTeam, awayTeam, league string) {
	normal := []footballFrame{
		{home: 0, away: 0, period: "1st Quarter", seconds: "15:00", label: "Game Start (0-0)"},
		{home: 0, away: 0, period: "1st Quarter", seconds: "14:55", label: "  (warm-up)"},
		{home: 7, away: 0, period: "1st Quarter", seconds: "9:30", label: "TD+XP! 7-0"},
		{home: 14, away: 0, period: "2nd Quarter", seconds: "7:15", label: "TD+XP! 14-0"},
		{home: 21, away: 0, period: "3rd Quarter", seconds: "10:40", label: "TD+XP! 21-0 (will be overturned)"},
	}

	confirmFrames := 9
	totalSteps := len(normal) + 1 + confirmFrames + 3
	step := 0

	for _, f := range normal {
		step++
		sendFootballFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}

	step++
	dropFrame := footballFrame{home: 14, away: 0, period: "3rd Quarter", seconds: "10:30", label: "OVERTURN! 21-0 → 14-0 (pending)"}
	sendFootballFrame(eid, homeTeam, awayTeam, league, dropFrame, step, totalSteps)
	time.Sleep(2 * time.Second)

	for i := 0; i < confirmFrames; i++ {
		step++
		secs := fmt.Sprintf("10:%02d", 28-i)
		f := footballFrame{home: 14, away: 0, period: "3rd Quarter", seconds: secs, label: fmt.Sprintf("  confirming overturn (%ds)", (i+2)*2)}
		sendFootballFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}

	post := []footballFrame{
		{home: 21, away: 0, period: "4th Quarter", seconds: "5:20", label: "TD+XP! 21-0 (real this time)"},
		{home: 21, away: 0, period: "4th Quarter", seconds: "0:00", label: "End of regulation"},
		{home: 21, away: 0, period: "Finished", seconds: "", label: "FINAL 21-0"},
	}
	for _, f := range post {
		step++
		sendFootballFrame(eid, homeTeam, awayTeam, league, f, step, totalSteps)
		time.Sleep(2 * time.Second)
	}
}

func sendFootballFrame(eid, homeTeam, awayTeam, league string, f footballFrame, step, total int) {
	ev := map[string]any{
		"info": map[string]any{
			"name":         fmt.Sprintf("%s vs %s", homeTeam, awayTeam),
			"period":       f.period,
			"status":       f.period,
			"seconds":      f.seconds,
			"league":       league,
			"category":     "american_football",
			"start_ts_utc": fmt.Sprintf("%d", time.Now().Add(-60*time.Minute).Unix()),
		},
		"team_info": map[string]any{
			"home": map[string]string{"name": homeTeam, "score": fmt.Sprintf("%d", f.home)},
			"away": map[string]string{"name": awayTeam, "score": fmt.Sprintf("%d", f.away)},
		},
	}

	payload := map[string]any{
		"updated":    time.Now().Format(time.RFC3339),
		"updated_ts": time.Now().Unix(),
		"events":     map[string]any{eid: ev},
	}

	send("/webhook/football", payload, step, total, f.label)
}

func send(path string, payload any, step, total int, label string) {
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("  [%d/%d] marshal error: %v\n", step, total, err)
		return
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(body)
	gz.Close()

	url := target + path
	resp, err := http.Post(url, "application/octet-stream", &buf)
	if err != nil {
		fmt.Printf("  [%d/%d] %s — POST error: %v\n", step, total, label, err)
		return
	}
	resp.Body.Close()
	fmt.Printf("  [%d/%d] %s — %d\n", step, total, label, resp.StatusCode)

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "    unexpected status %d for %s\n", resp.StatusCode, path)
	}
}
