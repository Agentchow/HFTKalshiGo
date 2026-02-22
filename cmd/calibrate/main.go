package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/charleschow/hft-trading/internal/core/strategy/soccer"
)

// football-data.co.uk CSV columns for Pinnacle odds + match results.
// See https://www.football-data.co.uk/notes.txt
type match struct {
	league string

	homeTeam string
	awayTeam string

	ftHomeGoals int
	ftAwayGoals int
	htHomeGoals int
	htAwayGoals int
	ftResult    string // H, D, A

	pinnacleHome float64 // PSH — decimal odds
	pinnacleDraw float64 // PSD
	pinnacleAway float64 // PSA

	homeReds int
	awayReds int
}

type calibrationResult struct {
	league      string
	totalGames  int
	htGames     int // games with valid HT data

	// Full-match calibration (pregame model vs actual FT result)
	pregameBrier float64

	// Half-time calibration (model at HT vs actual FT result)
	htBrier     float64
	htHomeErr   float64 // mean signed error: model - empirical
	htDrawErr   float64
	htAwayErr   float64
	htHomeBias  float64 // mean(model_home - pinnacle_pregame_home) at HT, to detect systematic shift
	htDrawBias  float64
	htAwayBias  float64

	// Bucketed calibration
	buckets []calBucket
}

type calBucket struct {
	label      string
	count      int
	meanPred   float64 // average predicted probability
	actualFreq float64 // fraction of times outcome actually occurred
}

type bucketAccum struct {
	sumPred float64
	count   int
	wins    int
}

var leagueURLs = map[string][]string{
	"EPL": {
		"https://www.football-data.co.uk/mmz4281/2425/E0.csv",
		"https://www.football-data.co.uk/mmz4281/2324/E0.csv",
		"https://www.football-data.co.uk/mmz4281/2223/E0.csv",
		"https://www.football-data.co.uk/mmz4281/2122/E0.csv",
	},
	"La Liga": {
		"https://www.football-data.co.uk/mmz4281/2425/SP1.csv",
		"https://www.football-data.co.uk/mmz4281/2324/SP1.csv",
		"https://www.football-data.co.uk/mmz4281/2223/SP1.csv",
		"https://www.football-data.co.uk/mmz4281/2122/SP1.csv",
	},
	"Serie A": {
		"https://www.football-data.co.uk/mmz4281/2425/I1.csv",
		"https://www.football-data.co.uk/mmz4281/2324/I1.csv",
		"https://www.football-data.co.uk/mmz4281/2223/I1.csv",
		"https://www.football-data.co.uk/mmz4281/2122/I1.csv",
	},
	"Bundesliga": {
		"https://www.football-data.co.uk/mmz4281/2425/D1.csv",
		"https://www.football-data.co.uk/mmz4281/2324/D1.csv",
		"https://www.football-data.co.uk/mmz4281/2223/D1.csv",
		"https://www.football-data.co.uk/mmz4281/2122/D1.csv",
	},
	"Ligue 1": {
		"https://www.football-data.co.uk/mmz4281/2425/F1.csv",
		"https://www.football-data.co.uk/mmz4281/2324/F1.csv",
		"https://www.football-data.co.uk/mmz4281/2223/F1.csv",
		"https://www.football-data.co.uk/mmz4281/2122/F1.csv",
	},
}

func main() {
	fmt.Println("=== Soccer Poisson Model Calibration ===")
	fmt.Println("Data source: football-data.co.uk (Pinnacle closing odds)")
	fmt.Println()

	var allResults []calibrationResult

	for league, urls := range leagueURLs {
		var matches []match
		for _, url := range urls {
			m, err := downloadAndParse(url, league)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARN: %s: %v\n", url, err)
				continue
			}
			matches = append(matches, m...)
		}
		if len(matches) == 0 {
			continue
		}

		result := calibrate(league, matches)
		allResults = append(allResults, result)
		printResult(result)
	}

	if len(allResults) > 0 {
		printSummary(allResults)
	}
}

func downloadAndParse(url, league string) ([]match, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	reader := csv.NewReader(resp.Body)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	required := []string{"HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR"}
	for _, r := range required {
		if _, ok := colIdx[r]; !ok {
			return nil, fmt.Errorf("missing column: %s", r)
		}
	}

	var matches []match
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		m := match{league: league}
		m.homeTeam = getCol(row, colIdx, "HomeTeam")
		m.awayTeam = getCol(row, colIdx, "AwayTeam")
		m.ftHomeGoals = getColInt(row, colIdx, "FTHG")
		m.ftAwayGoals = getColInt(row, colIdx, "FTAG")
		m.htHomeGoals = getColInt(row, colIdx, "HTHG")
		m.htAwayGoals = getColInt(row, colIdx, "HTAG")
		m.ftResult = getCol(row, colIdx, "FTR")

		m.pinnacleHome = getColFloat(row, colIdx, "PSH")
		m.pinnacleDraw = getColFloat(row, colIdx, "PSD")
		m.pinnacleAway = getColFloat(row, colIdx, "PSA")

		m.homeReds = getColInt(row, colIdx, "HR")
		m.awayReds = getColInt(row, colIdx, "AR")

		if m.pinnacleHome <= 1 || m.pinnacleDraw <= 1 || m.pinnacleAway <= 1 {
			continue
		}
		if m.ftResult == "" {
			continue
		}

		matches = append(matches, m)
	}

	return matches, nil
}

func calibrate(league string, matches []match) calibrationResult {
	result := calibrationResult{
		league:     league,
		totalGames: len(matches),
	}

	var (
		pregameBrierSum float64
		htBrierSum      float64
		htHomeErrSum    float64
		htDrawErrSum    float64
		htAwayErrSum    float64
	)

	// Calibration buckets: [0-10%, 10-20%, ..., 90-100%]
	homeBuckets := make([]bucketAccum, 10)
	drawBuckets := make([]bucketAccum, 10)
	awayBuckets := make([]bucketAccum, 10)

	for _, m := range matches {
		// Vig-free pregame probabilities from Pinnacle
		hImp := 1.0 / m.pinnacleHome
		dImp := 1.0 / m.pinnacleDraw
		aImp := 1.0 / m.pinnacleAway
		total := hImp + dImp + aImp
		pregH, pregD, pregA := hImp/total, dImp/total, aImp/total

		// Actual outcome
		var actH, actD, actA float64
		switch m.ftResult {
		case "H":
			actH = 1
		case "D":
			actD = 1
		case "A":
			actA = 1
		default:
			continue
		}

		// Pregame Brier score
		pregameBrierSum += (pregH-actH)*(pregH-actH) + (pregD-actD)*(pregD-actD) + (pregA-actA)*(pregA-actA)

		// HT calibration — only if we have valid HT data
		if m.htHomeGoals < 0 || m.htAwayGoals < 0 {
			continue
		}

		result.htGames++

		g0 := estimateG0(pregH, pregD, pregA)
		lamH, lamA := soccer.InferLambdas(pregH, pregD, pregA, g0)

		goalDiff := m.htHomeGoals - m.htAwayGoals
		probs := soccer.InplayProbs(lamH, lamA, 45.0, goalDiff, 2, 0, 0, true)

		htBrierSum += (probs.Home-actH)*(probs.Home-actH) +
			(probs.Draw-actD)*(probs.Draw-actD) +
			(probs.Away-actA)*(probs.Away-actA)

		htHomeErrSum += probs.Home - actH
		htDrawErrSum += probs.Draw - actD
		htAwayErrSum += probs.Away - actA

		// Bucket the HT predictions
		addToBucket(homeBuckets, probs.Home, actH)
		addToBucket(drawBuckets, probs.Draw, actD)
		addToBucket(awayBuckets, probs.Away, actA)
	}

	result.pregameBrier = pregameBrierSum / float64(result.totalGames)

	if result.htGames > 0 {
		n := float64(result.htGames)
		result.htBrier = htBrierSum / n
		result.htHomeErr = htHomeErrSum / n
		result.htDrawErr = htDrawErrSum / n
		result.htAwayErr = htAwayErrSum / n
	}

	for i := 0; i < 10; i++ {
		label := fmt.Sprintf("%d-%d%%", i*10, (i+1)*10)
		for _, buckets := range []struct {
			name string
			b    []bucketAccum
		}{
			{"home", homeBuckets},
			{"draw", drawBuckets},
			{"away", awayBuckets},
		} {
			b := buckets.b[i]
			if b.count == 0 {
				continue
			}
			result.buckets = append(result.buckets, calBucket{
				label:      fmt.Sprintf("%s %s", buckets.name, label),
				count:      b.count,
				meanPred:   b.sumPred / float64(b.count),
				actualFreq: float64(b.wins) / float64(b.count),
			})
		}
	}

	return result
}

func addToBucket(buckets []bucketAccum, pred, actual float64) {
	idx := int(pred * 10)
	if idx >= 10 {
		idx = 9
	}
	if idx < 0 {
		idx = 0
	}
	buckets[idx].sumPred += pred
	buckets[idx].count++
	if actual > 0.5 {
		buckets[idx].wins++
	}
}

func printResult(r calibrationResult) {
	fmt.Printf("── %s (%d matches, %d with HT data) ──\n", r.league, r.totalGames, r.htGames)
	fmt.Printf("  Pregame Brier score (Pinnacle):  %.4f\n", r.pregameBrier)
	fmt.Printf("  HT model Brier score:           %.4f\n", r.htBrier)
	improvement := (r.pregameBrier - r.htBrier) / r.pregameBrier * 100
	fmt.Printf("  Improvement over pregame:        %+.1f%%\n", improvement)
	fmt.Println()
	fmt.Printf("  HT mean signed error (bias):\n")
	fmt.Printf("    Home: %+.4f  Draw: %+.4f  Away: %+.4f\n",
		r.htHomeErr, r.htDrawErr, r.htAwayErr)
	fmt.Println()

	if len(r.buckets) > 0 {
		fmt.Println("  Calibration buckets (predicted vs actual):")
		fmt.Printf("  %-20s %6s %8s %8s %8s\n", "Bucket", "Count", "MeanPred", "ActFreq", "Error")
		for _, b := range r.buckets {
			err := b.meanPred - b.actualFreq
			fmt.Printf("  %-20s %6d %8.3f %8.3f %+8.3f\n",
				b.label, b.count, b.meanPred, b.actualFreq, err)
		}
	}
	fmt.Println()
}

func printSummary(results []calibrationResult) {
	fmt.Println("══════════════════════════════════════")
	fmt.Println("  OVERALL SUMMARY")
	fmt.Println("══════════════════════════════════════")

	var totalGames, totalHT int
	var pregameSum, htSum float64
	var homeErrSum, drawErrSum, awayErrSum float64

	for _, r := range results {
		totalGames += r.totalGames
		totalHT += r.htGames
		pregameSum += r.pregameBrier * float64(r.totalGames)
		htSum += r.htBrier * float64(r.htGames)
		homeErrSum += r.htHomeErr * float64(r.htGames)
		drawErrSum += r.htDrawErr * float64(r.htGames)
		awayErrSum += r.htAwayErr * float64(r.htGames)
	}

	avgPregame := pregameSum / float64(totalGames)
	avgHT := htSum / float64(totalHT)
	fmt.Printf("  Total matches:     %d (%d with HT)\n", totalGames, totalHT)
	fmt.Printf("  Avg pregame Brier: %.4f\n", avgPregame)
	fmt.Printf("  Avg HT Brier:      %.4f\n", avgHT)
	improvement := (avgPregame - avgHT) / avgPregame * 100
	fmt.Printf("  Improvement:       %+.1f%%\n", improvement)
	fmt.Println()
	fmt.Printf("  Overall HT bias:\n")
	fmt.Printf("    Home: %+.4f  Draw: %+.4f  Away: %+.4f\n",
		homeErrSum/float64(totalHT),
		drawErrSum/float64(totalHT),
		awayErrSum/float64(totalHT))
	fmt.Println()

	if math.Abs(drawErrSum/float64(totalHT)) > 0.02 {
		fmt.Println("  WARNING: Draw bias exceeds 2%. Consider adjusting Dixon-Coles rho.")
	}
	if math.Abs(homeErrSum/float64(totalHT)) > 0.02 {
		fmt.Println("  WARNING: Home bias exceeds 2%. Check time scaling or trailing-team adjustments.")
	}
	if math.Abs(awayErrSum/float64(totalHT)) > 0.02 {
		fmt.Println("  WARNING: Away bias exceeds 2%. Check time scaling or leading-team adjustments.")
	}
}

// estimateG0 estimates expected total goals from vig-free 1X2 probabilities.
// Uses the relationship between draw probability and total goals:
// higher draw probability implies fewer expected goals.
func estimateG0(homeP, drawP, awayP float64) float64 {
	// Binary search: find g0 where Poisson-implied draw probability matches
	lo, hi := 1.0, 5.0
	for i := 0; i < 50; i++ {
		mid := (lo + hi) / 2
		lamH, lamA := soccer.InferLambdas(homeP, drawP, awayP, mid)
		_, md, _ := poissonFullMatch(lamH, lamA)
		if md > drawP {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

func poissonFullMatch(lamH, lamA float64) (float64, float64, float64) {
	const maxG = 12
	pH := make([]float64, maxG+1)
	pA := make([]float64, maxG+1)
	for i := 0; i <= maxG; i++ {
		pH[i] = poissonPMF(i, lamH)
		pA[i] = poissonPMF(i, lamA)
	}
	var h, d, a float64
	for i := 0; i <= maxG; i++ {
		for j := 0; j <= maxG; j++ {
			p := pH[i] * pA[j]
			switch {
			case i > j:
				h += p
			case i == j:
				d += p
			default:
				a += p
			}
		}
	}
	t := h + d + a
	return h / t, d / t, a / t
}

func poissonPMF(k int, lambda float64) float64 {
	if lambda <= 0 {
		if k == 0 {
			return 1
		}
		return 0
	}
	logP := float64(k)*math.Log(lambda) - lambda
	for i := 2; i <= k; i++ {
		logP -= math.Log(float64(i))
	}
	return math.Exp(logP)
}

func getCol(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func getColInt(row []string, idx map[string]int, name string) int {
	s := getCol(row, idx, name)
	v, _ := strconv.Atoi(s)
	return v
}

func getColFloat(row []string, idx map[string]int, name string) float64 {
	s := getCol(row, idx, name)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
