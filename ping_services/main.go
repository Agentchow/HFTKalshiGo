// Ping Kalshi and GoalServe inplay servers to measure network latency.
//
// Measures DNS lookup, TCP connect, TLS handshake, and full HTTP round-trip
// times against the Kalshi API and GoalServe inplay endpoint.
//
// Usage:
//
//	go run ./ping_services                  # default: 20 requests
//	go run ./ping_services --env demo       # test Kalshi demo environment
//	go run ./ping_services -n 50            # 50 requests per endpoint
//	go run ./ping_services --ws             # also test Kalshi WebSocket latency
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charleschow/hft-trading/internal/adapters/kalshi_auth"
	"github.com/charleschow/hft-trading/internal/config"
)

const (
	kalshiStatusPath    = "/trade-api/v2/exchange/status"
	goalserveInplayURL  = "http://inplay.goalserve.com/inplay-hockey.gz"
	httpTimeout         = 10 * time.Second
	ipifyV4             = "https://api4.ipify.org"
	ipifyV6             = "https://api6.ipify.org"
)

func main() {
	env := flag.String("env", "prod", "Kalshi environment: prod or demo")
	n := flag.Int("n", 20, "Number of requests per endpoint")
	ws := flag.Bool("ws", false, "Also measure Kalshi WebSocket ping/pong latency")
	flag.Parse()

	cfg := config.Load()

	// Resolve env to base URL
	var kalshiBase string
	switch *env {
	case "demo":
		kalshiBase = "https://demo-api.kalshi.co"
	default:
		kalshiBase = "https://api.elections.kalshi.com"
	}

	// Show public IPs
	ipv4 := fetchURL(ipifyV4)
	if ipv4 == "" {
		ipv4 = "unavailable"
	}
	ipv6 := fetchURL(ipifyV6)
	if ipv6 == "" {
		ipv6 = "unavailable (no IPv6 connectivity)"
	}
	fmt.Printf("\nPinging services — IPv4: %s  |  IPv6: %s\n", ipv4, ipv6)

	wsURL := kalshiBase
	if strings.HasPrefix(kalshiBase, "https://") {
		wsURL = "wss://" + strings.TrimPrefix(kalshiBase, "https://") + "/trade-api/ws/v2"
	}
	pingKalshi(kalshiBase, wsURL, *env, *n, *ws, cfg)
	pingGoalServeInplay(*n)
	fmt.Println()
}

func pingKalshi(baseURL, wsURL, env string, n int, doWS bool, cfg *config.Config) {
	statusURL := baseURL + kalshiStatusPath

	fmt.Printf("\n%s\n", strings.Repeat("=", 55))
	fmt.Printf("  KALSHI (%s) — %s\n", strings.ToUpper(env), baseURL)
	fmt.Printf("%s\n", strings.Repeat("=", 55))

	// Cold start
	fmt.Println("\n  Cold-start request (DNS + TLS + HTTP):")
	if ms, code, err := measureHTTP(statusURL, nil); err != nil {
		fmt.Printf("    FAILED — %v\n", err)
	} else {
		fmt.Printf("    %.1f ms  (HTTP %d)\n", ms, code)
	}

	// Warm HTTP
	fmt.Printf("\n  Warm HTTP latency (%d requests, keep-alive):\n", n)
	client := &http.Client{Timeout: httpTimeout}
	if _, _, err := measureHTTP(statusURL, client); err != nil {
		fmt.Printf("  [!] Warm-up request failed: %v\n", err)
	} else {
		latencies := make([]float64, 0, n)
		pad := len(fmt.Sprintf("%d", n))
		for i := 1; i <= n; i++ {
			ms, code, err := measureHTTP(statusURL, client)
			if err != nil {
				fmt.Printf("  [%*d/%d]  FAILED — %v\n", pad, i, n, err)
				continue
			}
			latencies = append(latencies, ms)
			fmt.Printf("  [%*d/%d]  %7.1f ms  (HTTP %d)\n", pad, i, n, ms, code)
		}
		printStats(latencies, "Kalshi HTTP")
	}

	if doWS {
		fmt.Printf("\n  WebSocket ping/pong latency (%d pings):\n", n)
		wsLatencies := measureWSLatency(wsURL, n, env, cfg)
		if len(wsLatencies) > 0 {
			pad := len(fmt.Sprintf("%d", n))
			for i, ms := range wsLatencies {
				fmt.Printf("  [%*d/%d]  %7.1f ms  (WS ping/pong)\n", pad, i+1, n, ms)
			}
			printStats(wsLatencies, "Kalshi WebSocket")
		}
	}
}

func pingGoalServeInplay(n int) {
	fmt.Printf("\n%s\n", strings.Repeat("=", 55))
	fmt.Println("  GOALSERVE INPLAY")
	fmt.Printf("%s\n", strings.Repeat("=", 55))
	fmt.Printf("\n  Endpoint — %s\n", goalserveInplayURL)

	fmt.Println("\n  Cold-start request (DNS + TCP + HTTP):")
	if ms, code, err := measureHTTP(goalserveInplayURL, nil); err != nil {
		fmt.Printf("    FAILED — %v\n", err)
	} else {
		fmt.Printf("    %.1f ms  (HTTP %d)\n", ms, code)
	}

	fmt.Printf("\n  Warm HTTP latency (%d requests, keep-alive):\n", n)
	client := &http.Client{Timeout: httpTimeout}
	if _, _, err := measureHTTP(goalserveInplayURL, client); err != nil {
		fmt.Printf("  [!] Warm-up request failed: %v\n", err)
	} else {
		latencies := make([]float64, 0, n)
		pad := len(fmt.Sprintf("%d", n))
		for i := 1; i <= n; i++ {
			ms, code, err := measureHTTP(goalserveInplayURL, client)
			if err != nil {
				fmt.Printf("  [%*d/%d]  FAILED — %v\n", pad, i, n, err)
				continue
			}
			latencies = append(latencies, ms)
			fmt.Printf("  [%*d/%d]  %7.1f ms  (HTTP %d)\n", pad, i, n, ms, code)
		}
		printStats(latencies, "GoalServe Inplay HTTP")
	}
}

func measureHTTP(url string, client *http.Client) (ms float64, statusCode int, err error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	c := client
	if c == nil {
		c = &http.Client{Timeout: httpTimeout}
	}
	start := time.Now()
	resp, err := c.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	return float64(elapsed.Microseconds()) / 1000, resp.StatusCode, nil
}

func measureWSLatency(wsURL string, n int, env string, cfg *config.Config) []float64 {
	keyID := cfg.KalshiKeyID
	keyFile := cfg.KalshiKeyFile
	if env == "demo" {
		keyID = os.Getenv("DEMO_KEYID")
		keyFile = os.Getenv("DEMO_KEYFILE")
	} else {
		keyID = os.Getenv("PROD_KEYID")
		keyFile = os.Getenv("PROD_KEYFILE")
	}
	signer, err := kalshi_auth.NewSignerFromFile(keyID, keyFile)
	if err != nil || !signer.Enabled() {
		mode := "PROD"
		if env == "demo" {
			mode = "DEMO"
		}
		fmt.Printf("  [!] Kalshi credentials missing — set %s_KEYID and %s_KEYFILE\n", mode, mode)
		return nil
	}

	parsed, err := url.Parse(wsURL)
	if err != nil {
		fmt.Printf("  [!] Invalid WS URL: %v\n", err)
		return nil
	}
	path := parsed.Path
	if path == "" {
		path = "/trade-api/ws/v2"
	}
	header := signer.Headers("GET", path)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		fmt.Printf("  [!] WebSocket dial failed: %v\n", err)
		return nil
	}
	defer conn.Close()

	pongCh := make(chan struct{}, 1)
	conn.SetPongHandler(func(string) error {
		select {
		case pongCh <- struct{}{}:
		default:
		}
		return nil
	})

	// Run read loop so pong frames get processed (control frames are handled during read)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	latencies := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
			fmt.Printf("  [!] WS ping failed: %v\n", err)
			break
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		select {
		case <-pongCh:
			elapsed := time.Since(start)
			latencies = append(latencies, float64(elapsed.Microseconds())/1000)
		case <-time.After(5 * time.Second):
			fmt.Printf("  [!] WS pong timeout\n")
			return latencies
		}
	}
	return latencies
}

func printStats(latencies []float64, label string) {
	if len(latencies) < 2 {
		fmt.Printf("\n  Not enough %s samples for statistics.\n", label)
		return
	}
	sorted := make([]float64, len(latencies))
	copy(sorted, latencies)
	sort.Float64s(sorted)

	mean := 0.0
	for _, v := range latencies {
		mean += v
	}
	mean /= float64(len(latencies))

	variance := 0.0
	for _, v := range latencies {
		variance += (v - mean) * (v - mean)
	}
	variance /= float64(len(latencies) - 1)
	stdev := math.Sqrt(variance)

	median := sorted[len(sorted)/2]
	p95Idx := int(float64(len(sorted)) * 0.95)
	if p95Idx >= len(sorted) {
		p95Idx = len(sorted) - 1
	}
	p99Idx := int(float64(len(sorted)) * 0.99)
	if p99Idx >= len(sorted) {
		p99Idx = len(sorted) - 1
	}

	fmt.Printf("\n  --- %s Stats (%d requests) ---\n", label, len(latencies))
	fmt.Printf("  Min:    %7.1f ms\n", sorted[0])
	fmt.Printf("  Max:    %7.1f ms\n", sorted[len(sorted)-1])
	fmt.Printf("  Mean:   %7.1f ms\n", mean)
	fmt.Printf("  Median: %7.1f ms\n", median)
	fmt.Printf("  Stdev:  %7.1f ms\n", stdev)
	fmt.Printf("  p95:    %7.1f ms\n", sorted[p95Idx])
	fmt.Printf("  p99:    %7.1f ms\n", sorted[p99Idx])
}

func fetchURL(u string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var b [64]byte
	n, _ := resp.Body.Read(b[:])
	return strings.TrimSpace(string(b[:n]))
}
