package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/charleschow/hft-trading/internal/core/strategy/hockey"
)

type snapshot struct {
	gameID         string
	eventType      string
	homeScore      int
	awayScore      int
	timeRemain     float64
	pregameHome    float64
	pregameAway    float64
	kalshiHome     float64
	kalshiAway     float64
	actualOutcome  string
}

type trade struct {
	side  string
	cost  float64
	pnl   float64
	edge  float64
	tBucket string
}

type bucketStats struct {
	trades int
	wins   int
	pnl    float64
	edge   float64
}

func timeBucket(t float64) string {
	switch {
	case t > 50:
		return "50-60"
	case t > 40:
		return "40-50"
	case t > 30:
		return "30-40"
	case t > 20:
		return "20-30"
	case t > 10:
		return "10-20"
	default:
		return " 0-10"
	}
}

var bucketOrder = []string{"50-60", "40-50", "30-40", "20-30", "10-20", " 0-10"}

func skipEvent(et string) bool {
	switch et {
	case "GAME START", "GAME FINISH", "POWER PLAY", "POWER PLAY END":
		return true
	}
	return false
}

func parseCSV(path string) ([]snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}

	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}

	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	// First pass: build game_id → outcome map (outcome is only on GAME FINISH rows)
	outcomes := make(map[string]string)
	for _, row := range records {
		outcome := row[idx["actual_outcome"]]
		if outcome != "" {
			outcomes[row[idx["game_id"]]] = outcome
		}
	}

	// Second pass: collect eligible rows with outcome looked up by game_id
	var snaps []snapshot
	for _, row := range records {
		et := row[idx["event_type"]]
		if skipEvent(et) {
			continue
		}

		gid := row[idx["game_id"]]
		outcome := outcomes[gid]
		if outcome == "" || outcome == "shootout" {
			continue
		}

		hs, _ := strconv.Atoi(row[idx["home_score"]])
		as, _ := strconv.Atoi(row[idx["away_score"]])
		tr, _ := strconv.ParseFloat(row[idx["time_remain"]], 64)
		ph, _ := strconv.ParseFloat(row[idx["pregame_home_pct"]], 64)
		pa, _ := strconv.ParseFloat(row[idx["pregame_away_pct"]], 64)
		kh, _ := strconv.ParseFloat(row[idx["kalshi_home_pct_l"]], 64)
		ka, _ := strconv.ParseFloat(row[idx["kalshi_away_pct_l"]], 64)

		snaps = append(snaps, snapshot{
			gameID:        gid,
			eventType:     et,
			homeScore:     hs,
			awayScore:     as,
			timeRemain:    tr,
			pregameHome:   ph,
			pregameAway:   pa,
			kalshiHome:    kh,
			kalshiAway:    ka,
			actualOutcome: outcome,
		})
	}
	return snaps, nil
}

const feeRate = 0.02

type modelFunc func(teamStrength, timeRemain, currentLead float64) float64

func runBacktest(name string, snaps []snapshot, fn modelFunc, minEdge float64) {
	var trades []trade
	buckets := make(map[string]*bucketStats)
	for _, b := range bucketOrder {
		buckets[b] = &bucketStats{}
	}

	for _, s := range snaps {
		lead := float64(s.homeScore - s.awayScore)

		// Home evaluation
		modelHome := fn(s.pregameHome, s.timeRemain, lead)
		edge := modelHome - s.kalshiHome
		if edge >= minEdge {
			cost := s.kalshiHome
			fee := cost * feeRate
			var pnl float64
			if s.actualOutcome == "home_win" {
				pnl = 1.0 - cost - fee
			} else {
				pnl = -cost - fee
			}
			tb := timeBucket(s.timeRemain)
			trades = append(trades, trade{"home", cost, pnl, edge, tb})
			b := buckets[tb]
			b.trades++
			b.pnl += pnl
			b.edge += edge
			if pnl > 0 {
				b.wins++
			}
		}

		// Away evaluation
		modelAway := fn(s.pregameAway, s.timeRemain, -lead)
		edge = modelAway - s.kalshiAway
		if edge >= minEdge {
			cost := s.kalshiAway
			fee := cost * feeRate
			var pnl float64
			if s.actualOutcome == "away_win" {
				pnl = 1.0 - cost - fee
			} else {
				pnl = -cost - fee
			}
			tb := timeBucket(s.timeRemain)
			trades = append(trades, trade{"away", cost, pnl, edge, tb})
			b := buckets[tb]
			b.trades++
			b.pnl += pnl
			b.edge += edge
			if pnl > 0 {
				b.wins++
			}
		}
	}

	totalTrades := len(trades)
	if totalTrades == 0 {
		fmt.Printf("\n=== %s ===\nNo trades fired.\n", name)
		return
	}

	var wins int
	var totalPnL, totalEdge, totalFees float64
	for _, t := range trades {
		totalPnL += t.pnl
		totalEdge += t.edge
		totalFees += t.cost * feeRate
		if t.pnl > 0 {
			wins++
		}
	}

	fmt.Printf("\n=== %s ===\n", name)
	fmt.Printf("Min edge:      %.0f%%\n", minEdge*100)
	fmt.Printf("Fee rate:      %.0f%%\n", feeRate*100)
	fmt.Printf("Total trades:  %d\n", totalTrades)
	fmt.Printf("Wins / Losses: %d / %d  (%.1f%%)\n", wins, totalTrades-wins, 100*float64(wins)/float64(totalTrades))
	fmt.Printf("Total P&L:     $%.2f  (fees: $%.2f)\n", totalPnL, totalFees)
	fmt.Printf("Avg edge:      %.2f%%\n", 100*totalEdge/float64(totalTrades))
	fmt.Println()

	fmt.Printf("  %-8s  %6s  %6s  %8s  %9s  %9s\n", "Time", "Trades", "Wins", "Win%", "P&L", "Avg Edge")
	fmt.Printf("  %-8s  %6s  %6s  %8s  %9s  %9s\n", "--------", "------", "------", "--------", "---------", "---------")
	for _, bk := range bucketOrder {
		b := buckets[bk]
		if b.trades == 0 {
			fmt.Printf("  %-8s  %6d  %6d  %8s  %9s  %9s\n", bk, 0, 0, "-", "-", "-")
			continue
		}
		wr := 100 * float64(b.wins) / float64(b.trades)
		ae := 100 * b.edge / float64(b.trades)
		fmt.Printf("  %-8s  %6d  %6d  %7.1f%%  $%8.2f  %8.2f%%\n", bk, b.trades, b.wins, wr, b.pnl, ae)
	}
}

func main() {
	csvPath := "data/hockey_training_clean.csv"
	if len(os.Args) > 1 {
		csvPath = os.Args[1]
	}

	snaps, err := parseCSV(csvPath)
	if err != nil {
		log.Fatalf("failed to parse CSV: %v", err)
	}

	fmt.Printf("Loaded %d eligible rows\n", len(snaps))

	thresholds := []float64{0.01, 0.02, 0.03, 0.05, 0.07, 0.10}
	for _, t := range thresholds {
		runBacktest(fmt.Sprintf("V1  (edge >= %2.0f%%)", t*100), snaps, hockey.ProjectedOdds, t)
		runBacktest(fmt.Sprintf("V2  (edge >= %2.0f%%)", t*100), snaps, hockey.ProjectedOddsV2, t)
		runBacktest(fmt.Sprintf("V3  (edge >= %2.0f%%)", t*100), snaps, hockey.ProjectedOddsV3, t)
	}
}
