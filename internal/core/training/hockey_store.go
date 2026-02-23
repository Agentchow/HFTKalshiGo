package training

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

// HockeyRow holds all insert-time fields for a hockey training event.
type HockeyRow struct {
	Ts        time.Time
	GameID    string
	League    string
	HomeTeam  string
	AwayTeam  string
	NormHome  string
	NormAway  string
	EventType string
	HomeScore int
	AwayScore int
	Period    string
	TimeRemain float64

	HomePowerPlay bool
	AwayPowerPlay bool

	PregameHomePct *float64
	PregameAwayPct *float64
	PregameG0      *float64

	ActualOutcome *string
}

// HockeyOddsBackfill holds the delayed-fill odds columns for hockey.
// Nil pointers are written as SQL NULL.
type HockeyOddsBackfill struct {
	PinnacleHomePctL *float64
	PinnacleAwayPctL *float64

	KalshiHomePctL *float64
	KalshiAwayPctL *float64
}

// HockeyStore persists hockey training events in a FIFO SQLite database
// capped at ~3 GiB. Oldest 10% of rows are evicted when the budget is exceeded.
type HockeyStore struct {
	db           *sql.DB
	mu           sync.Mutex
	cachedSize   int64
	rowCount     int64
	evictCounter int
}

func OpenHockeyStore(path string) (*HockeyStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)

	for _, stmt := range []string{
		`PRAGMA auto_vacuum = INCREMENTAL`,
		`CREATE TABLE IF NOT EXISTS training_snapshots (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			ts                  TEXT    NOT NULL,
			game_id             TEXT    NOT NULL,
			league              TEXT,
			home_team           TEXT,
			away_team           TEXT,
			norm_home           TEXT,
			norm_away           TEXT,
			event_type          TEXT,
			home_score          INTEGER,
			away_score          INTEGER,
			period              TEXT,
			time_remain         REAL,

			home_power_play     INTEGER DEFAULT 0,
			away_power_play     INTEGER DEFAULT 0,

			pregame_home_pct    REAL,
			pregame_away_pct    REAL,
			pregame_g0          REAL,

			pinnacle_home_pct_l REAL,
			pinnacle_away_pct_l REAL,

			kalshi_home_pct_l   REAL,
			kalshi_away_pct_l   REAL,

			actual_outcome      TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ht_game_id ON training_snapshots(game_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ht_ts ON training_snapshots(ts)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema (%s): %w", stmt, err)
		}
	}

	// Migrations: add columns if missing (errors ignored for existing columns).
	db.Exec(`ALTER TABLE training_snapshots ADD COLUMN home_power_play INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE training_snapshots ADD COLUMN away_power_play INTEGER DEFAULT 0`)

	var size int64
	row := db.QueryRow(`SELECT COALESCE(page_count * page_size, 0) FROM pragma_page_count(), pragma_page_size()`)
	if err := row.Scan(&size); err != nil {
		db.Close()
		return nil, fmt.Errorf("read db size: %w", err)
	}

	var count int64
	row = db.QueryRow(`SELECT COUNT(*) FROM training_snapshots`)
	if err := row.Scan(&count); err != nil {
		db.Close()
		return nil, fmt.Errorf("read row count: %w", err)
	}

	telemetry.Plainf("hockey training store: opened %s  db_bytes=%d  rows=%d", path, size, count)

	return &HockeyStore{db: db, cachedSize: size, rowCount: count}, nil
}

func (s *HockeyStore) Insert(row HockeyRow) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`INSERT INTO training_snapshots (
			ts, game_id, league, home_team, away_team, norm_home, norm_away,
			event_type, home_score, away_score, period, time_remain,
			home_power_play, away_power_play,
			pregame_home_pct, pregame_away_pct, pregame_g0,
			actual_outcome
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.Ts.UTC().Format(time.RFC3339Nano),
		row.GameID,
		row.League,
		row.HomeTeam,
		row.AwayTeam,
		row.NormHome,
		row.NormAway,
		row.EventType,
		row.HomeScore,
		row.AwayScore,
		row.Period,
		row.TimeRemain,
		boolToInt(row.HomePowerPlay),
		boolToInt(row.AwayPowerPlay),
		round3(row.PregameHomePct),
		round3(row.PregameAwayPct),
		round3(row.PregameG0),
		row.ActualOutcome,
	)
	if err != nil {
		return 0, fmt.Errorf("hockey training insert: %w", err)
	}

	id, _ := res.LastInsertId()
	s.rowCount++

	if s.rowCount%100 == 0 {
		s.refreshSize()
		if s.cachedSize > maxStoreBytes {
			s.evict()
		}
	}

	return id, nil
}

// BackfillOdds updates the odds columns for a previously inserted row.
func (s *HockeyStore) BackfillOdds(rowID int64, odds HockeyOddsBackfill) {
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		_, err := s.db.Exec(
			`UPDATE training_snapshots SET
				pinnacle_home_pct_l = ?,
				pinnacle_away_pct_l = ?,
				kalshi_home_pct_l   = ?,
				kalshi_away_pct_l   = ?
			WHERE id = ?`,
			round3(odds.PinnacleHomePctL),
			round3(odds.PinnacleAwayPctL),
			round3(odds.KalshiHomePctL),
			round3(odds.KalshiAwayPctL),
			rowID,
		)
		if err != nil {
			telemetry.Warnf("hockey training backfill (row %d): %v", rowID, err)
		}
	}()
}

func (s *HockeyStore) refreshSize() {
	var size int64
	row := s.db.QueryRow(`SELECT COALESCE(page_count * page_size, 0) FROM pragma_page_count(), pragma_page_size()`)
	if err := row.Scan(&size); err == nil {
		s.cachedSize = size
	}
}

func (s *HockeyStore) evict() {
	toDelete := int64(float64(s.rowCount) * evictPct)
	if toDelete < 1 {
		toDelete = 1
	}

	res, err := s.db.Exec(
		`DELETE FROM training_snapshots WHERE id IN (
			SELECT id FROM training_snapshots ORDER BY id ASC LIMIT ?
		)`, toDelete,
	)
	if err != nil {
		telemetry.Warnf("hockey training evict: %v", err)
		return
	}

	deleted, _ := res.RowsAffected()
	s.rowCount -= deleted
	s.evictCounter++

	telemetry.Infof("hockey training: evicted %d rows (target %d)", deleted, toDelete)

	if s.evictCounter%vacuumInterval == 0 {
		s.db.Exec(`PRAGMA incremental_vacuum`)
	}

	s.refreshSize()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *HockeyStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
