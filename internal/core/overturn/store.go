package overturn

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"

	_ "modernc.org/sqlite"
)

// Row holds all columns for a single overturn event.
type Row struct {
	Ts           time.Time
	Sport        string
	GameID       string
	League       string
	HomeTeam     string
	AwayTeam     string
	EventType    string // "OVERTURN PENDING", "OVERTURN CONFIRMED", "OVERTURN REJECTED"
	OldHomeScore int
	OldAwayScore int
	NewHomeScore int
	NewAwayScore int
	Period       string
	TimeRemain   float64

	KalshiHomeYes *float64
	KalshiAwayYes *float64
	KalshiDrawYes *float64 // soccer only

	Bet365HomePct *float64
	Bet365AwayPct *float64
	Bet365DrawPct *float64 // soccer only
}

// Store persists overturn events in a SQLite database.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS overturns (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			ts              TEXT    NOT NULL,
			sport           TEXT    NOT NULL,
			game_id         TEXT    NOT NULL,
			league          TEXT,
			home_team       TEXT,
			away_team       TEXT,
			event_type      TEXT    NOT NULL,
			old_home_score  INTEGER NOT NULL,
			old_away_score  INTEGER NOT NULL,
			new_home_score  INTEGER NOT NULL,
			new_away_score  INTEGER NOT NULL,
			period          TEXT,
			time_remain     REAL,

			kalshi_home_yes REAL,
			kalshi_away_yes REAL,
			kalshi_draw_yes REAL,

			bet365_home_pct REAL,
			bet365_away_pct REAL,
			bet365_draw_pct REAL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ot_game_id ON overturns(game_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ot_ts ON overturns(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_ot_sport ON overturns(sport)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema (%s): %w", stmt, err)
		}
	}

	var count int64
	row := db.QueryRow(`SELECT COUNT(*) FROM overturns`)
	if err := row.Scan(&count); err != nil {
		db.Close()
		return nil, fmt.Errorf("read row count: %w", err)
	}

	telemetry.Infof("Started Overturn db  path=%s  rows=%d", path, count)

	return &Store{db: db}, nil
}

func (s *Store) Insert(row Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO overturns (
			ts, sport, game_id, league, home_team, away_team,
			event_type,
			old_home_score, old_away_score,
			new_home_score, new_away_score,
			period, time_remain,
			kalshi_home_yes, kalshi_away_yes, kalshi_draw_yes,
			bet365_home_pct, bet365_away_pct, bet365_draw_pct
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.Ts.UTC().Format(time.RFC3339Nano),
		row.Sport,
		row.GameID,
		row.League,
		row.HomeTeam,
		row.AwayTeam,
		row.EventType,
		row.OldHomeScore,
		row.OldAwayScore,
		row.NewHomeScore,
		row.NewAwayScore,
		row.Period,
		row.TimeRemain,
		row.KalshiHomeYes,
		row.KalshiAwayYes,
		row.KalshiDrawYes,
		row.Bet365HomePct,
		row.Bet365AwayPct,
		row.Bet365DrawPct,
	)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
