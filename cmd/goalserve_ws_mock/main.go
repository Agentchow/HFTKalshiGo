// goalserve_ws_mock simulates the GoalServe Inplay WebSocket API locally.
// It serves a fake auth endpoint and per-sport WebSocket feeds that send
// realistic "avl" and "updt" messages to exercise the full WS pipeline.
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
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

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

var soccerGames = []*mockGame{
	{id: "MOCK-S1", sport: "soccer", cmpName: "English Premier League", home: "Arsenal", away: "Chelsea", pc: 1, startTime: time.Now().Unix()},
	{id: "MOCK-S2", sport: "soccer", cmpName: "La Liga", home: "Real Madrid", away: "Barcelona", pc: 1, startTime: time.Now().Unix()},
}

var hockeyGames = []*mockGame{
	{id: "MOCK-H1", sport: "hockey", cmpName: "NHL", home: "Boston Bruins", away: "Toronto Maple Leafs", pc: 1, startTime: time.Now().Unix()},
	{id: "MOCK-H2", sport: "hockey", cmpName: "AHL", home: "Providence Bruins", away: "Toronto Marlies", pc: 1, startTime: time.Now().Unix()},
}

var footballGames = []*mockGame{
	{id: "MOCK-F1", sport: "amfootball", cmpName: "NFL", home: "Kansas City Chiefs", away: "Philadelphia Eagles", pc: 1, startTime: time.Now().Unix()},
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
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", handleAuth)
	mux.HandleFunc("/ws/", handleWS)

	fmt.Fprintf(os.Stderr, "GoalServe WS Mock listening on %s\n", listenAddr)
	fmt.Fprintf(os.Stderr, "  Auth: http://localhost%s/auth\n", listenAddr)
	fmt.Fprintf(os.Stderr, "  WS:   ws://localhost%s/ws/{sport}\n", listenAddr)

	// Advance game time in background.
	go tickGames()

	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
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

	fmt.Fprintf(os.Stderr, "[%s] client connected\n", sport)

	// Send initial avl.
	sendAvl(conn, sport, games)

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

// tickGames advances elapsed time and occasionally scores goals.
func tickGames() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	allGames := append(append(soccerGames, hockeyGames...), footballGames...)

	for range ticker.C {
		for _, g := range allGames {
			g.mu.Lock()
			g.et++

			// Random scoring: ~2% chance per tick.
			if rand.Float64() < 0.02 {
				if rand.Float64() < 0.5 {
					g.homeScore++
				} else {
					g.awayScore++
				}
			}

			// Period transitions.
			switch g.sport {
			case "soccer":
				if g.pc == 1 && g.et >= 2700 { // 45 min
					g.pc = 2
					g.et = 0
				} else if g.pc == 2 {
					g.pc = 3
					g.et = 0
				} else if g.pc == 3 && g.et >= 2700 { // 45 min of 2nd half
					g.pc = 255
				}
			case "hockey":
				if g.pc == 1 && g.et >= 1200 { // 20 min
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
