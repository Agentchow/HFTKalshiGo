package goalserve

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"
	_ "modernc.org/sqlite"
)

// InplayCollector periodically polls GoalServe's inplay feed and stores
// (pregame_state, live_state, inplay_odds) tuples for training a
// Pinnacle proxy model. Each row is one observation of how the inplay
// book prices a particular game state.
type InplayCollector struct {
	client   *Client
	db       *sql.DB
	sports   []string
	interval time.Duration

	mu       sync.Mutex
	pregame  map[string]*matchPregame // match_id → pregame odds
	lastSnap map[string]snapKey       // match_id → last inserted state (for dedup)

	stopOnce sync.Once
	stopCh   chan struct{}
}

type matchPregame struct {
	league  string
	homeP   float64
	drawP   float64
	awayP   float64
	g0      float64
	fetched bool
}

type snapKey struct {
	homeScore int
	awayScore int
	half      string
}

// NewInplayCollector creates a collector that polls every interval and
// writes samples to dbPath. Sports should be e.g. ["soccer", "hockey"].
func NewInplayCollector(client *Client, dbPath string, sports []string, interval time.Duration) (*InplayCollector, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open collector db: %w", err)
	}

	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS matches (
			match_id         TEXT PRIMARY KEY,
			sport            TEXT NOT NULL,
			home_team        TEXT NOT NULL,
			away_team        TEXT NOT NULL,
			league           TEXT NOT NULL DEFAULT '',
			pregame_home_pct REAL NOT NULL DEFAULT 0,
			pregame_draw_pct REAL NOT NULL DEFAULT 0,
			pregame_away_pct REAL NOT NULL DEFAULT 0,
			expected_goals   REAL NOT NULL DEFAULT 0,
			first_seen       TEXT NOT NULL,
			last_seen        TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS inplay_samples (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			sport            TEXT NOT NULL,
			match_id         TEXT NOT NULL,
			league           TEXT NOT NULL DEFAULT '',
			home_score       INTEGER NOT NULL,
			away_score       INTEGER NOT NULL,
			timer            TEXT NOT NULL,
			minutes_elapsed  REAL NOT NULL DEFAULT 0,
			half             TEXT NOT NULL,
			red_cards_home   INTEGER NOT NULL DEFAULT 0,
			red_cards_away   INTEGER NOT NULL DEFAULT 0,
			pregame_home_pct REAL NOT NULL DEFAULT 0,
			pregame_draw_pct REAL NOT NULL DEFAULT 0,
			pregame_away_pct REAL NOT NULL DEFAULT 0,
			expected_goals   REAL NOT NULL DEFAULT 0,
			home_pct         REAL NOT NULL,
			draw_pct         REAL NOT NULL,
			away_pct         REAL NOT NULL,
			bookmaker        TEXT NOT NULL DEFAULT '',
			collected_at     TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_samples_match ON inplay_samples(match_id)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			return nil, fmt.Errorf("create schema: %w", err)
		}
	}

	return &InplayCollector{
		client:   client,
		db:       db,
		sports:   sports,
		interval: interval,
		pregame:  make(map[string]*matchPregame),
		lastSnap: make(map[string]snapKey),
		stopCh:   make(chan struct{}),
	}, nil
}

func (ic *InplayCollector) Start() {
	go ic.loop()
	telemetry.Infof("inplay_collector: started  sports=%v  interval=%s", ic.sports, ic.interval)
}

func (ic *InplayCollector) Stop() {
	ic.stopOnce.Do(func() {
		close(ic.stopCh)
		ic.db.Close()
	})
}

// RegisterPregame lets the system feed pregame odds into the collector
// so every sample row includes the team strength baseline.
func (ic *InplayCollector) RegisterPregame(matchID, league string, homeP, drawP, awayP, g0 float64) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.pregame[matchID] = &matchPregame{
		league:  league,
		homeP:   homeP,
		drawP:   drawP,
		awayP:   awayP,
		g0:      g0,
		fetched: true,
	}
}

func (ic *InplayCollector) loop() {
	ticker := time.NewTicker(ic.interval)
	defer ticker.Stop()

	ic.poll()

	for {
		select {
		case <-ticker.C:
			ic.poll()
		case <-ic.stopCh:
			return
		}
	}
}

func (ic *InplayCollector) poll() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, sport := range ic.sports {
		odds, err := ic.client.FetchInplayOdds(ctx, sport)
		if err != nil {
			telemetry.Warnf("inplay_collector: fetch %s: %v", sport, err)
			continue
		}

		now := time.Now().UTC().Format(time.RFC3339)
		inserted := 0
		for _, o := range odds {
			if ic.isDuplicate(o) {
				continue
			}

			pg := ic.getPregame(o.MatchID)
			minElapsed := parseMinutesElapsed(o.Timer, o.Half)

			_, err := ic.db.ExecContext(ctx,
				`INSERT INTO inplay_samples
				(sport, match_id, league, home_score, away_score,
				 timer, minutes_elapsed, half, red_cards_home, red_cards_away,
				 pregame_home_pct, pregame_draw_pct, pregame_away_pct, expected_goals,
				 home_pct, draw_pct, away_pct, bookmaker, collected_at)
				VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?,?, ?,?,?,?,?)`,
				sport, o.MatchID, pg.league,
				o.HomeScore, o.AwayScore,
				o.Timer, minElapsed, o.Half, 0, 0,
				pg.homeP, pg.drawP, pg.awayP, pg.g0,
				o.HomeWinPct, o.DrawPct, o.AwayWinPct,
				o.Bookmaker, now,
			)
			if err != nil {
				telemetry.Warnf("inplay_collector: insert: %v", err)
				continue
			}
			inserted++

			ic.upsertMatch(ctx, o, sport, pg, now)
			ic.recordSnap(o)
		}

		if inserted > 0 {
			telemetry.Debugf("inplay_collector: stored %d/%d %s samples", inserted, len(odds), sport)
		}
	}
}

func (ic *InplayCollector) isDuplicate(o InplayOdds) bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	last, ok := ic.lastSnap[o.MatchID]
	if !ok {
		return false
	}
	return last.homeScore == o.HomeScore &&
		last.awayScore == o.AwayScore &&
		last.half == o.Half
}

func (ic *InplayCollector) recordSnap(o InplayOdds) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.lastSnap[o.MatchID] = snapKey{
		homeScore: o.HomeScore,
		awayScore: o.AwayScore,
		half:      o.Half,
	}
}

func (ic *InplayCollector) getPregame(matchID string) *matchPregame {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if pg, ok := ic.pregame[matchID]; ok {
		return pg
	}
	return &matchPregame{}
}

func (ic *InplayCollector) upsertMatch(ctx context.Context, o InplayOdds, sport string, pg *matchPregame, now string) {
	_, _ = ic.db.ExecContext(ctx,
		`INSERT INTO matches (match_id, sport, home_team, away_team, league,
			pregame_home_pct, pregame_draw_pct, pregame_away_pct, expected_goals,
			first_seen, last_seen)
		VALUES (?,?,?,?,?, ?,?,?,?, ?,?)
		ON CONFLICT(match_id) DO UPDATE SET last_seen=excluded.last_seen`,
		o.MatchID, sport, o.HomeTeam, o.AwayTeam, pg.league,
		pg.homeP, pg.drawP, pg.awayP, pg.g0,
		now, now,
	)
}

// parseMinutesElapsed converts GoalServe timer strings like "34:12" to
// minutes elapsed as a float. Returns 0 on parse failure.
func parseMinutesElapsed(timer, half string) float64 {
	timer = strings.TrimSpace(timer)
	if timer == "" {
		return 0
	}

	parts := strings.SplitN(timer, ":", 2)
	mins, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}

	if len(parts) == 2 {
		secs, err := strconv.ParseFloat(parts[1], 64)
		if err == nil {
			mins += secs / 60.0
		}
	}

	h := strings.ToLower(strings.TrimSpace(half))
	if strings.Contains(h, "2nd") || strings.Contains(h, "second") {
		mins += 45
	}

	return mins
}
