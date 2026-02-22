package ticker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/charleschow/hft-trading/internal/adapters/outbound/kalshi_http"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// defaultSeriesTickers is the hardcoded fallback when no config file is found.
var defaultSeriesTickers = map[events.Sport][]string{
	events.SportHockey: {
		"KXAHLGAME", "KXNHLGAME", "KXKHLGAME", "KXSHLGAME", "KXLIIGAGAME",
		"KXWOMHOCKEY", "KXWOWHOCKEY",
	},
	events.SportSoccer: {
		"KXEPLGAME", "KXUCLGAME", "KXLALIGAGAME", "KXBUNDESLIGAGAME",
		"KXSERIEAGAME", "KXLIGUE1GAME", "KXLIGAMXGAME", "KXALEAGUEGAME",
		"KXJLEAGUEGAME", "KXDIMAYORGAME", "KXAFCCLGAME", "KXSAUDIPLGAME",
		"KXUELGAME", "KXUECLGAME", "KXARGPREMDIVGAME", "KXBRASILEIROGAME",
		"KXSUPERLIGGAME", "KXEKSTRAKLASAGAME", "KXHNLGAME",
		"KXBUNDESLIGA2GAME", "KXLALIGA2GAME", "KXEREDIVISIEGAME",
		"KXSERIEBGAME", "KXBELGIANPLGAME", "KXEFLCHAMPIONSHIPGAME",
		"KXLIGAPORTUGALGAME", "KXDENSUPERLIGAGAME",
	},
	events.SportFootball: {
		"KXNFLGAME", "KXNCAAFGAME",
	},
}

// sportConfigDir maps a Sport value to its directory name inside the config dir.
var sportConfigDir = map[events.Sport]string{
	events.SportHockey:   "Hockey",
	events.SportSoccer:   "Soccer",
	events.SportFootball: "Football",
}

type tickersConfig struct {
	SeriesTickers []string `json:"series_tickers"`
}

// loadSeriesTickers reads {dir}/{SportDir}/tickers_config.json and returns
// the series_tickers list (uppercased). Returns the hardcoded default if the
// dir is empty or the file cannot be read.
func loadSeriesTickers(dir string, sport events.Sport) []string {
	if dir == "" {
		return defaultSeriesTickers[sport]
	}
	subdir, ok := sportConfigDir[sport]
	if !ok {
		return defaultSeriesTickers[sport]
	}
	path := filepath.Join(dir, subdir, "tickers_config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		telemetry.Warnf("ticker: config file %s not found, using hardcoded defaults for %s", path, sport)
		return defaultSeriesTickers[sport]
	}
	var cfg tickersConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		telemetry.Warnf("ticker: failed to parse %s: %v, using hardcoded defaults for %s", path, err, sport)
		return defaultSeriesTickers[sport]
	}
	if len(cfg.SeriesTickers) == 0 {
		telemetry.Warnf("ticker: %s has empty series_tickers, using hardcoded defaults for %s", path, sport)
		return defaultSeriesTickers[sport]
	}
	upper := make([]string, len(cfg.SeriesTickers))
	for i, t := range cfg.SeriesTickers {
		upper[i] = strings.ToUpper(t)
	}
	telemetry.Infof("ticker: loaded %d series for %s", len(upper), sport)
	return upper
}

// ResolvedTickers is the result of resolving a game to Kalshi tickers.
type ResolvedTickers struct {
	HomeTicker string
	AwayTicker string
	DrawTicker string // soccer only
}

// Resolver fetches Kalshi markets and matches them to games by team name.
type Resolver struct {
	client        *kalshi_http.Client
	mu            sync.RWMutex
	markets       map[events.Sport][]kalshi_http.Market
	lastFetch     map[events.Sport]time.Time
	aliases       map[events.Sport]map[string]string
	seriesTickers map[events.Sport][]string
	sfGroup       singleflight.Group
}

func NewResolver(client *kalshi_http.Client, tickersConfigDir string, sports ...events.Sport) *Resolver {
	if len(sports) == 0 {
		sports = []events.Sport{events.SportHockey, events.SportSoccer, events.SportFootball}
	}

	series := make(map[events.Sport][]string, len(sports))
	aliases := make(map[events.Sport]map[string]string, len(sports))
	for _, sport := range sports {
		series[sport] = loadSeriesTickers(tickersConfigDir, sport)
		switch sport {
		case events.SportHockey:
			aliases[sport] = HockeyAliases
		case events.SportSoccer:
			aliases[sport] = SoccerAliases
		default:
			aliases[sport] = map[string]string{}
		}
	}

	return &Resolver{
		client:        client,
		markets:       make(map[events.Sport][]kalshi_http.Market),
		lastFetch:     make(map[events.Sport]time.Time),
		seriesTickers: series,
		aliases:       aliases,
	}
}

const marketCacheTTL = 10 * time.Minute

// Markets whose expiration is more than this far from the game's start time
// are rejected in favour of a closer match. Matches the Python codebase's
// PREGAME_KALSHI_MATCH_WINDOW_SEC (12h hockey, 16h soccer).
const matchWindowHockey = 12 * time.Hour
const matchWindowSoccer = 16 * time.Hour

// RefreshMarkets fetches all open markets for a sport from Kalshi.
func (r *Resolver) RefreshMarkets(ctx context.Context, sport events.Sport) error {
	series := r.seriesTickers[sport]
	if len(series) == 0 {
		return nil
	}

	var all []kalshi_http.Market
	for _, s := range series {
		markets, err := r.client.GetMarkets(ctx, s)
		if err != nil {
			telemetry.Warnf("ticker: failed to fetch series %s: %v", s, err)
			continue
		}
		all = append(all, markets...)
	}

	r.mu.Lock()
	r.markets[sport] = all
	r.lastFetch[sport] = time.Now()
	r.mu.Unlock()

	telemetry.Infof("ticker: fetched %d markets for %s (%d series)", len(all), sport, len(series))
	return nil
}

func (r *Resolver) ensureFresh(ctx context.Context, sport events.Sport) {
	r.mu.RLock()
	last := r.lastFetch[sport]
	r.mu.RUnlock()

	if time.Since(last) > marketCacheTTL {
		r.sfGroup.Do(string(sport), func() (any, error) {
			return nil, r.RefreshMarkets(ctx, sport)
		})
	}
}

// Resolve finds Kalshi tickers for a game identified by home/away team names.
// gameStartedAt is the scheduled kick-off from GoalServe (start_ts_utc), or
// the webhook arrival time as fallback when GoalServe doesn't provide it.
// Used to disambiguate when the same two teams play twice (doubleheader).
func (r *Resolver) Resolve(ctx context.Context, sport events.Sport, homeTeam, awayTeam string, gameStartedAt time.Time) *ResolvedTickers {
	r.ensureFresh(ctx, sport)

	aliases := r.aliases[sport]
	homeNorm := Normalize(homeTeam, aliases)
	awayNorm := Normalize(awayTeam, aliases)

	r.mu.RLock()
	markets := r.markets[sport]
	r.mu.RUnlock()

	window := matchWindowHockey
	if sport == events.SportSoccer {
		window = matchWindowSoccer
		return r.resolveSoccer(markets, homeNorm, awayNorm, aliases, gameStartedAt, window)
	}
	return r.resolveHockey(markets, homeNorm, awayNorm, aliases, gameStartedAt, window)
}

// matchCandidate pairs a matching market with its temporal distance from the game.
type matchCandidate struct {
	market   kalshi_http.Market
	timeDiff time.Duration // abs(market expiry - game start)
}

// resolveHockey matches hockey/football markets.
// When multiple markets match the same team pair, picks the one whose
// expiration is closest to gameStartedAt (doubleheader disambiguation).
func (r *Resolver) resolveHockey(markets []kalshi_http.Market, homeNorm, awayNorm string, aliases map[string]string, gameStartedAt time.Time, window time.Duration) *ResolvedTickers {
	var candidates []matchCandidate

	for _, m := range markets {
		t1, t2 := teamNamesFromTitle(m.Title, aliases)
		if t1 == "" || t2 == "" {
			if m.Subtitle != "" {
				t1, t2 = teamNamesFromTitle(m.Subtitle, aliases)
			}
			if t1 == "" || t2 == "" {
				continue
			}
		}

		pairMatches := (t1 == homeNorm && t2 == awayNorm) ||
			(t1 == awayNorm && t2 == homeNorm) ||
			(fuzzyContains(t1, homeNorm) && fuzzyContains(t2, awayNorm)) ||
			(fuzzyContains(t1, awayNorm) && fuzzyContains(t2, homeNorm))

		if !pairMatches {
			continue
		}

		candidates = append(candidates, matchCandidate{
			market:   m,
			timeDiff: absTimeDiff(gameStartedAt, parseMarketExpiry(m)),
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	// If only one candidate, use it directly
	// If multiple, pick the one closest in time
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.timeDiff < best.timeDiff {
			best = c
		}
	}

	// If best is outside the window AND there's only one candidate, still use it.
	// If multiple and best is outside window, warn but still use it.
	if len(candidates) > 1 && best.timeDiff > window {
		telemetry.Warnf("ticker: doubleheader best match for %s vs %s is %v away (window=%v)",
			homeNorm, awayNorm, best.timeDiff, window)
	}

	result := &ResolvedTickers{}
	yesTeam := normalizeYesSubTitle(best.market.YesSubTitle, aliases)
	if fuzzyContains(yesTeam, homeNorm) {
		result.HomeTicker = best.market.Ticker
	} else if fuzzyContains(yesTeam, awayNorm) {
		result.AwayTicker = best.market.Ticker
	}
	return result
}

// soccerEventCandidate is a parsed soccer event group that matched the team pair.
type soccerEventCandidate struct {
	drawTicker  string
	teamMarkets []kalshi_http.Market
	timeDiff    time.Duration
}

// resolveSoccer matches soccer markets (3 markets per event: home/draw/away).
// When multiple events match the same team pair, picks the one closest in time.
func (r *Resolver) resolveSoccer(markets []kalshi_http.Market, homeNorm, awayNorm string, aliases map[string]string, gameStartedAt time.Time, window time.Duration) *ResolvedTickers {
	byEvent := make(map[string][]kalshi_http.Market)
	for _, m := range markets {
		if m.EventTicker != "" {
			byEvent[m.EventTicker] = append(byEvent[m.EventTicker], m)
		}
	}

	var candidates []soccerEventCandidate

	for _, group := range byEvent {
		var drawTicker string
		var teamMarkets []kalshi_http.Market

		for _, m := range group {
			if strings.HasSuffix(strings.ToUpper(m.Ticker), "-TIE") {
				drawTicker = m.Ticker
			} else {
				teamMarkets = append(teamMarkets, m)
			}
		}

		if len(teamMarkets) != 2 {
			continue
		}

		names := make([]string, 0, 2)
		for _, m := range teamMarkets {
			name := normalizeYesSubTitle(m.YesSubTitle, aliases)
			if name == "" {
				t1, _ := teamNamesFromTitle(m.Title, aliases)
				name = t1
			}
			names = append(names, name)
		}

		if len(names) < 2 {
			continue
		}

		matchesPair := (fuzzyContains(names[0], homeNorm) && fuzzyContains(names[1], awayNorm)) ||
			(fuzzyContains(names[0], awayNorm) && fuzzyContains(names[1], homeNorm))

		if !matchesPair {
			for _, m := range group {
				t1, t2 := teamNamesFromTitle(m.Title, aliases)
				if t1 != "" && t2 != "" {
					matchesPair = (fuzzyContains(t1, homeNorm) && fuzzyContains(t2, awayNorm)) ||
						(fuzzyContains(t1, awayNorm) && fuzzyContains(t2, homeNorm))
					if matchesPair {
						break
					}
				}
			}
		}

		if !matchesPair {
			continue
		}

		// Use the max expiry across the group's markets as the event time
		var maxExpiry time.Time
		for _, m := range group {
			if t := parseMarketExpiry(m); !t.IsZero() && t.After(maxExpiry) {
				maxExpiry = t
			}
		}

		candidates = append(candidates, soccerEventCandidate{
			drawTicker:  drawTicker,
			teamMarkets: teamMarkets,
			timeDiff:    absTimeDiff(gameStartedAt, maxExpiry),
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	// Pick closest event by time
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.timeDiff < best.timeDiff {
			best = c
		}
	}

	if len(candidates) > 1 && best.timeDiff > window {
		telemetry.Warnf("ticker: soccer doubleheader best match for %s vs %s is %v away (window=%v)",
			homeNorm, awayNorm, best.timeDiff, window)
	}

	result := &ResolvedTickers{DrawTicker: best.drawTicker}
	for _, m := range best.teamMarkets {
		yesTeam := normalizeYesSubTitle(m.YesSubTitle, aliases)
		if fuzzyContains(yesTeam, homeNorm) {
			result.HomeTicker = m.Ticker
		} else {
			result.AwayTicker = m.Ticker
		}
	}
	return result
}

// AllTickers returns all resolved ticker strings (non-empty).
func (rt *ResolvedTickers) AllTickers() []string {
	var out []string
	if rt.HomeTicker != "" {
		out = append(out, rt.HomeTicker)
	}
	if rt.AwayTicker != "" {
		out = append(out, rt.AwayTicker)
	}
	if rt.DrawTicker != "" {
		out = append(out, rt.DrawTicker)
	}
	return out
}

// teamNamesFromTitle parses "Team1 at Team2 Winner?" into (norm1, norm2).
func teamNamesFromTitle(title string, aliases map[string]string) (string, string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", ""
	}
	for _, sep := range []string{" at ", " vs. ", " vs "} {
		if idx := strings.Index(title, sep); idx >= 0 {
			t1 := strings.TrimSpace(title[:idx])
			rest := strings.TrimSpace(title[idx+len(sep):])
			rest = strings.TrimSuffix(rest, " Winner?")
			rest = strings.TrimSuffix(rest, " Winner")
			rest = strings.TrimSuffix(rest, "?")
			rest = strings.TrimSpace(rest)
			if t1 != "" && rest != "" {
				return Normalize(t1, aliases), Normalize(rest, aliases)
			}
		}
	}
	return "", ""
}

func normalizeYesSubTitle(label string, aliases map[string]string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	for _, suffix := range []string{" to Win", " Winner", " winner", " Wins", " Win"} {
		if strings.HasSuffix(label, suffix) {
			label = strings.TrimSuffix(label, suffix)
			label = strings.TrimSpace(label)
			break
		}
	}
	return Normalize(label, aliases)
}

func fuzzyContains(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.Contains(a, b) || strings.Contains(b, a)
}

// parseMarketExpiry extracts the market's expected expiration time.
// Prefers expected_expiration_time over close_time, matching the Python logic.
func parseMarketExpiry(m kalshi_http.Market) time.Time {
	for _, field := range []string{m.ExpectedExpirationTime, m.CloseTime} {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		// Kalshi uses RFC3339 / ISO8601: "2026-02-21T04:00:00Z"
		t, err := time.Parse(time.RFC3339, field)
		if err == nil {
			return t
		}
		// Try without timezone suffix
		t, err = time.Parse("2006-01-02T15:04:05", field)
		if err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func absTimeDiff(a, b time.Time) time.Duration {
	if a.IsZero() || b.IsZero() {
		return time.Duration(1<<63 - 1) // max duration — worst possible match
	}
	d := a.Sub(b)
	if d < 0 {
		return -d
	}
	return d
}
