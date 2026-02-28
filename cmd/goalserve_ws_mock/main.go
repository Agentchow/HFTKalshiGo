// goalserve_ws_mock simulates the GoalServe Inplay WebSocket API locally.
// It serves a fake auth endpoint and per-sport WebSocket feeds that send
// realistic "avl" and "updt" messages to exercise the full WS pipeline.
//
// On startup it queries the Kalshi API for active markets and uses those
// team names so the engine can match them to existing GameContexts.
//
// Usage:
//
//	go run cmd/goalserve_ws_mock/main.go
//
// Then set these env vars before running cmd/main.go:
//
//	GOALSERVE_WS_ENABLED=true
//	GOALSERVE_WS_AUTH_URL=http://localhost:9200/auth
//	GOALSERVE_WS_URL=ws://localhost:9200/ws
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/config"
	"github.com/gorilla/websocket"
)

const listenAddr = ":9200"

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

type mockGame struct {
	mu        sync.Mutex
	id        string
	sport     string
	cmpName   string
	home      string
	away      string
	homeScore int
	awayScore int
	pc        int
	et        int
	startTime int64
}

var (
	soccerGames   []*mockGame
	hockeyGames   []*mockGame
	footballGames []*mockGame
)

var seriesToLeague = map[string]string{
	"KXEPLGAME":             "English Premier League",
	"KXUCLGAME":             "UEFA Champions League",
	"KXLALIGAGAME":          "La Liga",
	"KXBUNDESLIGAGAME":      "Bundesliga",
	"KXSERIEAGAME":          "Serie A",
	"KXLIGUE1GAME":          "Ligue 1",
	"KXLIGAMXGAME":          "Liga MX",
	"KXMLSGAME":             "MLS",
	"KXNHLGAME":             "NHL",
	"KXAHLGAME":             "AHL",
	"KXKHLGAME":             "KHL",
	"KXSHLGAME":             "SHL",
	"KXLIIGAGAME":           "Liiga",
	"KXELHGAME":             "ELH",
	"KXNLGAME":              "NL",
	"KXDELGAME":             "DEL",
	"KXNFLGAME":             "NFL",
	"KXNCAAFGAME":           "NCAAF",
	"KXEFLCHAMPIONSHIPGAME": "EFL Championship",
	"KXEREDIVISIEGAME":      "Eredivisie",
	"KXSERIEBGAME":          "Serie B",
	"KXBELGIANPLGAME":       "Belgian Pro League",
	"KXLIGAPORTUGALGAME":    "Liga Portugal",
	"KXDENSUPERLIGAGAME":    "Danish Superliga",
	"KXSAUDIPLGAME":         "Saudi Pro League",
	"KXUELGAME":             "UEFA Europa League",
	"KXUECLGAME":            "UEFA Europa Conference League",
}

func gamesBySport(sport string) []*mockGame {
	switch sport {
	case "soccer":
		return soccerGames
	case "hockey":
		return hockeyGames
	case "amfootball":
		return footballGames
	default:
		return nil
	}
}

func main() {
	fmt.Fprintln(os.Stderr, "Fetching active Kalshi markets...")
	discoverGames()

	fmt.Fprintf(os.Stderr, "\nDiscovered games:\n")
	for _, g := range soccerGames {
		fmt.Fprintf(os.Stderr, "  [soccer]   %s vs %s (%s)\n", g.home, g.away, g.cmpName)
	}
	for _, g := range hockeyGames {
		fmt.Fprintf(os.Stderr, "  [hockey]   %s vs %s (%s)\n", g.home, g.away, g.cmpName)
	}
	for _, g := range footballGames {
		fmt.Fprintf(os.Stderr, "  [football] %s vs %s (%s)\n", g.home, g.away, g.cmpName)
	}

	if len(soccerGames) == 0 && len(hockeyGames) == 0 && len(footballGames) == 0 {
		fmt.Fprintln(os.Stderr, "  No games found on Kalshi — mock will serve empty feeds")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth", handleAuth)
	mux.HandleFunc("/ws/", handleWS)

	fmt.Fprintf(os.Stderr, "\nGoalServe WS Mock listening on %s\n", listenAddr)
	fmt.Fprintf(os.Stderr, "  Auth: http://localhost%s/auth\n", listenAddr)
	fmt.Fprintf(os.Stderr, "  WS:   ws://localhost%s/ws/{sport}\n", listenAddr)

	go tickGames()

	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}

func discoverGames() {
	cfg := config.Load()
	signer, err := kalshi_auth.NewSignerFromFile(cfg.KalshiKeyID, cfg.KalshiKeyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kalshi auth: %v — using fallback games\n", err)
		useFallbackGames()
		return
	}
	client := kalshi_http.NewClient(cfg.KalshiBaseURL, signer, cfg.RateDivisor)
	ctx := context.Background()

	soccerSeries := []string{
		"KXEPLGAME", "KXUCLGAME", "KXLALIGAGAME", "KXBUNDESLIGAGAME",
		"KXSERIEAGAME", "KXLIGUE1GAME", "KXMLSGAME",
	}
	hockeySeries := []string{
		"KXNHLGAME", "KXAHLGAME", "KXKHLGAME", "KXSHLGAME", "KXNLGAME",
		"KXLIIGAGAME", "KXELHGAME", "KXDELGAME",
	}
	footballSeries := []string{
		"KXNFLGAME", "KXNCAAFGAME",
	}

	soccerGames = findGames(ctx, client, "soccer", soccerSeries, true, 2)
	hockeyGames = findGames(ctx, client, "hockey", hockeySeries, false, 2)
	footballGames = findGames(ctx, client, "amfootball", footballSeries, false, 1)

	if len(hockeyGames) == 0 && len(soccerGames) == 0 && len(footballGames) == 0 {
		fmt.Fprintln(os.Stderr, "No games discovered from Kalshi — using fallback games")
		useFallbackGames()
	}
}

func useFallbackGames() {
	now := time.Now().Unix()
	hockeyGames = []*mockGame{
		{id: "MOCK-H1", sport: "hockey", cmpName: "NHL", home: "New York Rangers", away: "Pittsburgh Penguins", pc: 1, startTime: now},
	}
	soccerGames = []*mockGame{
		{id: "MOCK-S1", sport: "soccer", cmpName: "English Premier League", home: "Arsenal", away: "Chelsea", pc: 1, startTime: now},
	}
}

func findGames(ctx context.Context, client *kalshi_http.Client, sport string, seriesList []string, isSoccer bool, maxGames int) []*mockGame {
	var games []*mockGame
	now := time.Now()
	cutoff := now.Add(48 * time.Hour)
	count := 0

	for _, series := range seriesList {
		if count >= maxGames {
			break
		}
		markets, err := client.GetMarkets(ctx, series)
		if err != nil {
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

		for _, group := range byEvent {
			if count >= maxGames {
				break
			}
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

			count++
			games = append(games, &mockGame{
				id:        fmt.Sprintf("MOCK-%s%d", strings.ToUpper(sport[:1]), count),
				sport:     sport,
				cmpName:   league,
				home:      home,
				away:      away,
				pc:        1,
				startTime: now.Unix(),
			})
		}
	}
	return games
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
	if len(teamMarkets) != 2 {
		return "", ""
	}

	t1, t2 := parseTitle(teamMarkets[0].Title)
	if t1 != "" && t2 != "" {
		away, home = t1, t2
		return
	}

	home = cleanTeamName(teamMarkets[0].YesSubTitle)
	away = cleanTeamName(teamMarkets[1].YesSubTitle)
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

func handleAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": "mock-token-" + fmt.Sprint(time.Now().Unix())})
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/ws/")
	sport := strings.Split(path, "?")[0]

	games := gamesBySport(sport)
	if games == nil {
		http.Error(w, "unknown sport", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	fmt.Fprintf(os.Stderr, "[%s] client connected (%d games)\n", sport, len(games))

	if len(games) > 0 {
		sendAvl(conn, sport, games)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		for _, g := range games {
			msg := buildUpdt(g)
			data, _ := json.Marshal(msg)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] write error: %v\n", sport, err)
				return
			}
		}
	}
}

func sendAvl(conn *websocket.Conn, sport string, games []*mockGame) {
	avl := map[string]any{
		"mt": "avl",
		"sp": sport,
		"dt": time.Now().Format("01-02-2006 15:04:05"),
		"bm": "mock",
		"evts": func() []map[string]any {
			var evts []map[string]any
			for _, g := range games {
				evts = append(evts, map[string]any{
					"id":       g.id,
					"cmp_name": g.cmpName,
					"t1":       map[string]string{"n": g.home},
					"t2":       map[string]string{"n": g.away},
					"pc":       g.pc,
				})
			}
			return evts
		}(),
	}
	data, _ := json.Marshal(avl)
	conn.WriteMessage(websocket.TextMessage, data)
}

func buildUpdt(g *mockGame) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()

	stats := buildStats(g)

	msg := map[string]any{
		"mt":       "updt",
		"sp":       g.sport,
		"bm":       "mock",
		"st":       g.startTime,
		"uptd":     time.Now().Format("02.01.2006 15:04:05"),
		"pt":       fmt.Sprint(time.Now().UnixMilli()),
		"id":       g.id,
		"cmp_name": g.cmpName,
		"t1":       map[string]string{"n": g.home},
		"t2":       map[string]string{"n": g.away},
		"et":       g.et,
		"stp":      0,
		"bl":       0,
		"pc":       g.pc,
		"sc":       "21000",
		"cms":      []any{},
		"stat":     "",
		"stats":    stats,
		"odds":     buildMockOdds(g),
	}
	return msg
}

func buildStats(g *mockGame) map[string]any {
	switch g.sport {
	case "soccer":
		return map[string]any{
			"a":  [2]int{g.homeScore, g.awayScore},
			"r":  [2]int{0, 0},
			"c":  [2]int{0, 0},
			"y":  [2]int{0, 0},
			"h1": [2]int{g.homeScore, g.awayScore},
		}
	case "hockey":
		return map[string]any{
			"P1": [2]int{g.homeScore, g.awayScore},
			"P2": [2]int{0, 0},
			"P3": [2]int{0, 0},
			"T":  [2]int{g.homeScore, g.awayScore},
		}
	default:
		return map[string]any{
			"a": [2]int{g.homeScore, g.awayScore},
		}
	}
}

func buildMockOdds(g *mockGame) []map[string]any {
	if g.sport == "soccer" {
		return []map[string]any{{
			"id": 50246,
			"bl": 0,
			"o": []map[string]any{
				{"n": "1", "v": 2.5, "b": 0},
				{"n": "X", "v": 3.2, "b": 0},
				{"n": "2", "v": 2.8, "b": 0},
			},
		}}
	}
	return []map[string]any{{
		"id": 86,
		"bl": 0,
		"o": []map[string]any{
			{"n": "1", "v": 1.8, "b": 0},
			{"n": "2", "v": 2.0, "b": 0},
		},
	}}
}

func tickGames() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	allGames := append(append(soccerGames, hockeyGames...), footballGames...)

	for range ticker.C {
		for _, g := range allGames {
			g.mu.Lock()
			g.et++

			if rand.Float64() < 0.02 {
				if rand.Float64() < 0.5 {
					g.homeScore++
				} else {
					g.awayScore++
				}
			}

			switch g.sport {
			case "soccer":
				if g.pc == 1 && g.et >= 2700 {
					g.pc = 2
					g.et = 0
				} else if g.pc == 2 {
					g.pc = 3
					g.et = 0
				} else if g.pc == 3 && g.et >= 2700 {
					g.pc = 255
				}
			case "hockey":
				if g.pc == 1 && g.et >= 1200 {
					g.pc = 2
					g.et = 0
				} else if g.pc == 2 && g.et >= 1200 {
					g.pc = 3
					g.et = 0
				} else if g.pc == 3 && g.et >= 1200 {
					g.pc = 255
				}
			}

			g.mu.Unlock()
		}
	}
}
