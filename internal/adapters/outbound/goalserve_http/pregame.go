package goalserve_http

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/core/odds"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const (
	goalserveBase    = "http://www.goalserve.com/getfeed"
	soccerPregameCat = "soccer_10"
	requestTimeout   = 45 * time.Second
	rateLimitSec     = 10
)

var preferredBookmakers = []string{
	"pinnacle", "pin", "pinnaclesports",
	"bet365", "williamhill", "william hill",
	"1xbet", "marathon", "unibet",
}

// PregameClient fetches pregame odds from the GoalServe HTTP API.
type PregameClient struct {
	apiKey     string
	httpClient *http.Client
	mu         sync.Mutex
	lastReq    time.Time
}

func NewPregameClient(apiKey string) *PregameClient {
	return &PregameClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// FetchSoccerPregame fetches all soccer pregame odds from GoalServe and returns
// a slice of parsed matches. The caller should match by team name.
func (c *PregameClient) FetchSoccerPregame() ([]odds.PregameOdds, error) {
	c.rateLimit()

	url := fmt.Sprintf("%s/%s/getodds/soccer?cat=%s&json=1", goalserveBase, c.apiKey, soccerPregameCat)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("goalserve pregame fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("goalserve pregame: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("goalserve pregame read: %w", err)
	}

	matches, err := parseSoccerPregameJSON(body)
	if err != nil {
		return nil, err
	}

	telemetry.Infof("goalserve: fetched %d soccer pregame matches", len(matches))
	return matches, nil
}

func (c *PregameClient) rateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	elapsed := time.Since(c.lastReq)
	if elapsed < time.Duration(rateLimitSec)*time.Second && !c.lastReq.IsZero() {
		time.Sleep(time.Duration(rateLimitSec)*time.Second - elapsed)
	}
	c.lastReq = time.Now()
}

// --- JSON response parsing ---

type gsResponse struct {
	Scores json.RawMessage `json:"scores"`
}

type gsScores struct {
	Categories json.RawMessage `json:"categories"`
	Category   json.RawMessage `json:"category"`
}

type gsCategory struct {
	Name    string          `json:"name"`
	Matches json.RawMessage `json:"matches"`
	Match   json.RawMessage `json:"match"`
}

type gsMatchContainer struct {
	Match json.RawMessage `json:"match"`
}

type gsMatch struct {
	ID          json.RawMessage `json:"id"`
	LocalTeam   gsTeam          `json:"localteam"`
	VisitorTeam gsTeam          `json:"visitorteam"`
	Odds        json.RawMessage `json:"odds"`
}

type gsTeam struct {
	Name string `json:"name"`
}

func parseSoccerPregameJSON(data []byte) ([]odds.PregameOdds, error) {
	// Strip UTF-8 BOM if present
	data = stripBOM(data)

	var top gsResponse
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("goalserve pregame parse: %w", err)
	}

	scoresRaw := top.Scores
	if scoresRaw == nil {
		scoresRaw = data
	}

	cats := extractCategories(scoresRaw)

	var out []odds.PregameOdds
	for _, cat := range cats {
		matches := extractMatches(cat)
		for _, m := range matches {
			parsed := extract1X2andOU(m)
			if parsed == nil {
				continue
			}
			parsed.HomeTeam = strings.TrimSpace(m.LocalTeam.Name)
			parsed.AwayTeam = strings.TrimSpace(m.VisitorTeam.Name)
			if parsed.HomeTeam == "" || parsed.AwayTeam == "" {
				continue
			}
			out = append(out, *parsed)
		}
	}
	return out, nil
}

func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

func extractCategories(scoresRaw json.RawMessage) []json.RawMessage {
	var scores gsScores
	if err := json.Unmarshal(scoresRaw, &scores); err != nil {
		return nil
	}

	raw := scores.Categories
	if raw == nil {
		raw = scores.Category
	}
	if raw == nil {
		return nil
	}

	// Could be array or single object
	if raw[0] == '[' {
		var arr []json.RawMessage
		json.Unmarshal(raw, &arr)
		return arr
	}
	return []json.RawMessage{raw}
}

func extractMatches(catRaw json.RawMessage) []gsMatch {
	var cat gsCategory
	if err := json.Unmarshal(catRaw, &cat); err != nil {
		return nil
	}

	matchesRaw := cat.Matches
	if matchesRaw == nil {
		matchesRaw = cat.Match
	}
	if matchesRaw == nil {
		return nil
	}

	// Could be {"match": [...]} or directly [...]
	if matchesRaw[0] == '{' {
		var container gsMatchContainer
		if err := json.Unmarshal(matchesRaw, &container); err == nil && container.Match != nil {
			matchesRaw = container.Match
		}
	}

	if matchesRaw[0] == '[' {
		var arr []gsMatch
		json.Unmarshal(matchesRaw, &arr)
		return arr
	}
	// Single match object
	var single gsMatch
	if err := json.Unmarshal(matchesRaw, &single); err == nil {
		return []gsMatch{single}
	}
	return nil
}

// --- 1X2 and O/U extraction ---

type oddsType struct {
	ID         json.RawMessage `json:"id"`
	Value      string          `json:"value"`
	Name       string          `json:"name"`
	Bookmaker  json.RawMessage `json:"bookmaker"`
	Bookmakers json.RawMessage `json:"bookmakers"`
}

type bookmaker struct {
	Name  string          `json:"name"`
	Stop  json.RawMessage `json:"stop"`
	Odd   json.RawMessage `json:"odd"`
	Odds  json.RawMessage `json:"odds"`
	Total json.RawMessage `json:"total"`
}

type oddEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Total string `json:"total"`
}

type totalGroup struct {
	Name   string          `json:"name"`
	Value  string          `json:"value"`
	IsMain string          `json:"ismain"`
	Odd    json.RawMessage `json:"odd"`
	Odds   json.RawMessage `json:"odds"`
}

func extract1X2andOU(m gsMatch) *odds.PregameOdds {
	oddsTypes := parseOddsTypes(m.Odds)
	if len(oddsTypes) == 0 {
		return nil
	}

	// Find Match Winner market (id=1 or name match)
	var mw *oddsType
	for i, ot := range oddsTypes {
		idStr := strings.TrimSpace(rawToString(ot.ID))
		name := strings.TrimSpace(ot.Value)
		if name == "" {
			name = strings.TrimSpace(ot.Name)
		}
		if idStr == "1" || name == "Match Winner" || name == "3Way Result" || name == "2Way Result" {
			mw = &oddsTypes[i]
			break
		}
	}
	if mw == nil {
		return nil
	}

	bm := pickBookmaker(mw.Bookmaker, mw.Bookmakers)
	if bm == nil {
		return nil
	}

	oddEntries := parseOddEntries(bm)
	var homeDec, drawDec, awayDec float64
	for _, o := range oddEntries {
		name := strings.TrimSpace(o.Name)
		val := parseFloat(o.Value)
		if val <= 1.0 {
			continue
		}
		switch name {
		case "1", "Home":
			homeDec = val
		case "X", "Draw":
			drawDec = val
		case "2", "Away":
			awayDec = val
		}
	}
	if homeDec == 0 || drawDec == 0 || awayDec == 0 {
		return nil
	}

	h, d, a := odds.RemoveVig3(homeDec, drawDec, awayDec)
	g0 := extractG0FromOU(oddsTypes)

	return &odds.PregameOdds{
		HomeWinPct: h,
		DrawPct:    d,
		AwayWinPct: a,
		G0:         g0,
	}
}

func extractG0FromOU(oddsTypes []oddsType) float64 {
	var ouMarket *oddsType
	for i, ot := range oddsTypes {
		idStr := strings.TrimSpace(rawToString(ot.ID))
		name := strings.TrimSpace(ot.Value)
		if name == "" {
			name = strings.TrimSpace(ot.Name)
		}
		nameLow := strings.ToLower(name)
		if strings.Contains(nameLow, "over") || idStr == "5" {
			ouMarket = &oddsTypes[i]
			break
		}
	}
	if ouMarket == nil {
		return 2.5
	}

	bm := pickBookmaker(ouMarket.Bookmaker, ouMarket.Bookmakers)
	if bm == nil {
		return 2.5
	}

	// Strategy 1: grouped totals
	if over, under, ok := extractFromTotals(bm, "2.5"); ok {
		_, pUnder := odds.RemoveVig2(over, under)
		return math.Round(odds.InferG0FromOU25(pUnder)*1000) / 1000
	}

	// Strategy 2: flat odds with "total" attribute
	oddEntries := parseOddEntries(bm)
	var overDec, underDec float64
	for _, o := range oddEntries {
		if strings.TrimSpace(o.Total) != "2.5" {
			continue
		}
		val := parseFloat(o.Value)
		if val <= 1.0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(o.Name)) {
		case "over":
			overDec = val
		case "under":
			underDec = val
		}
	}
	if overDec > 0 && underDec > 0 {
		_, pUnder := odds.RemoveVig2(overDec, underDec)
		return math.Round(odds.InferG0FromOU25(pUnder)*1000) / 1000
	}

	return 2.5
}

func extractFromTotals(bm *bookmaker, targetTotal string) (over, under float64, ok bool) {
	if bm.Total == nil {
		return 0, 0, false
	}

	var groups []totalGroup
	if bm.Total[0] == '[' {
		json.Unmarshal(bm.Total, &groups)
	} else {
		var single totalGroup
		if err := json.Unmarshal(bm.Total, &single); err == nil {
			groups = []totalGroup{single}
		}
	}

	for _, g := range groups {
		totalVal := strings.TrimSpace(g.Name)
		if totalVal == "" {
			totalVal = strings.TrimSpace(g.Value)
		}
		if totalVal != targetTotal {
			continue
		}

		innerRaw := g.Odd
		if innerRaw == nil {
			innerRaw = g.Odds
		}
		if innerRaw == nil {
			continue
		}

		var entries []oddEntry
		if innerRaw[0] == '[' {
			json.Unmarshal(innerRaw, &entries)
		} else {
			var single oddEntry
			if err := json.Unmarshal(innerRaw, &single); err == nil {
				entries = []oddEntry{single}
			}
		}

		for _, o := range entries {
			val := parseFloat(o.Value)
			if val <= 1.0 {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(o.Name)) {
			case "over":
				over = val
			case "under":
				under = val
			}
		}
		if over > 0 && under > 0 {
			return over, under, true
		}
	}
	return 0, 0, false
}

// --- Helpers ---

func parseOddsTypes(raw json.RawMessage) []oddsType {
	if raw == nil {
		return nil
	}

	// Could be {"ts": ..., "type": [...]} or directly [...]
	if raw[0] == '{' {
		var wrapper struct {
			Type json.RawMessage `json:"type"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Type != nil {
			raw = wrapper.Type
		}
	}

	if raw[0] == '[' {
		var arr []oddsType
		json.Unmarshal(raw, &arr)
		return arr
	}

	var single oddsType
	if err := json.Unmarshal(raw, &single); err == nil {
		return []oddsType{single}
	}
	return nil
}

func pickBookmaker(bm1, bm2 json.RawMessage) *bookmaker {
	raw := bm1
	if raw == nil {
		raw = bm2
	}
	if raw == nil {
		return nil
	}

	var bms []bookmaker
	if raw[0] == '[' {
		json.Unmarshal(raw, &bms)
	} else {
		var single bookmaker
		if err := json.Unmarshal(raw, &single); err == nil {
			bms = []bookmaker{single}
		}
	}

	var active []bookmaker
	for _, b := range bms {
		if !isStopped(b.Stop) {
			active = append(active, b)
		}
	}
	if len(active) == 0 {
		return nil
	}

	for _, pref := range preferredBookmakers {
		for i, b := range active {
			if strings.Contains(strings.ToLower(b.Name), pref) {
				return &active[i]
			}
		}
	}
	return &active[0]
}

func isStopped(raw json.RawMessage) bool {
	if raw == nil {
		return false
	}
	s := strings.Trim(string(raw), `"`)
	low := strings.ToLower(strings.TrimSpace(s))
	return low == "true" || low == "1" || low == "yes"
}

func parseOddEntries(bm *bookmaker) []oddEntry {
	raw := bm.Odd
	if raw == nil {
		raw = bm.Odds
	}
	if raw == nil {
		return nil
	}
	if raw[0] == '[' {
		var arr []oddEntry
		json.Unmarshal(raw, &arr)
		return arr
	}
	var single oddEntry
	if err := json.Unmarshal(raw, &single); err == nil {
		return []oddEntry{single}
	}
	return nil
}

func rawToString(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	s := string(raw)
	return strings.Trim(s, `"`)
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}
