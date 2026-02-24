package training

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/telemetry"

	_ "modernc.org/sqlite"
)

func round5(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*100000) / 100000
	return &r
}

const (
	maxStoreBytes  int64   = 3 << 30 // 3 GiB
	evictPct       float64 = 0.10    // evict oldest 10% of rows
	vacuumInterval         = 10      // incremental vacuum every N evictions
)

// SoccerRow holds all insert-time fields for a training event.
type SoccerRow struct {
	Ts           time.Time
	GameID       string
	League       string
	HomeTeam     string
	AwayTeam     string
	NormHome     string
	NormAway     string
	Half         string
	EventType    string
	HomeScore    int
	AwayScore    int
	TimeRemain   float64
	RedCardsHome int
	RedCardsAway int

	PregameHomePct *float64
	PregameDrawPct *float64
	PregameAwayPct *float64
	PregameG0      *float64

	ActualOutcome *string
}

// OddsBackfill holds the delayed-fill odds columns.
// Nil pointers are written as SQL NULL.
type OddsBackfill struct {
	PinnacleHomePctL *float64
	PinnacleDrawPctL *float64
	PinnacleAwayPctL *float64

	KalshiHomePctL *float64
	KalshiDrawPctL *float64
	KalshiAwayPctL *float64
}

// Store persists soccer training events in a FIFO SQLite database
// capped at ~3 GiB. Oldest 10% of rows are evicted when the budget is exceeded.
type Store struct {
	db           *sql.DB
	mu           sync.Mutex
	cachedSize   int64
	rowCount     int64
	evictCounter int
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
		`PRAGMA auto_vacuum = INCREMENTAL`,
		`CREATE TABLE IF NOT EXISTS soccer_training (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			ts               TEXT    NOT NULL,
			game_id          TEXT    NOT NULL,
			league           TEXT,
			home_team        TEXT,
			away_team        TEXT,
			norm_home        TEXT,
			norm_away        TEXT,
			half             TEXT,
			event_type       TEXT,
			home_score       INTEGER,
			away_score       INTEGER,
			time_remain      REAL,
			red_cards_home   INTEGER,
			red_cards_away   INTEGER,

			pregame_home_pct REAL,
			pregame_draw_pct REAL,
			pregame_away_pct REAL,
			pregame_g0       REAL,

			pinnacle_home_pct_l REAL,
			pinnacle_draw_pct_l REAL,
			pinnacle_away_pct_l REAL,

			kalshi_home_pct_l   REAL,
			kalshi_draw_pct_l   REAL,
			kalshi_away_pct_l   REAL,

			actual_outcome   TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_st_game_id ON soccer_training(game_id)`,
		`CREATE INDEX IF NOT EXISTS idx_st_ts ON soccer_training(ts)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema (%s): %w", stmt, err)
		}
	}

	var size int64
	row := db.QueryRow(`SELECT COALESCE(page_count * page_size, 0) FROM pragma_page_count(), pragma_page_size()`)
	if err := row.Scan(&size); err != nil {
		db.Close()
		return nil, fmt.Errorf("read db size: %w", err)
	}

	var count int64
	row = db.QueryRow(`SELECT COUNT(*) FROM soccer_training`)
	if err := row.Scan(&count); err != nil {
		db.Close()
		return nil, fmt.Errorf("read row count: %w", err)
	}

	telemetry.Infof("Started Soccer Training db  path=%s  db_bytes=%d  rows=%d", path, size, count)

	return &Store{db: db, cachedSize: size, rowCount: count}, nil
}

// Insert stores a training row synchronously and returns the row ID for
// later backfill. The caller (game goroutine) is already serialized, so
// the insert is safe without additional locking on the caller side.
func (s *Store) Insert(row SoccerRow) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`INSERT INTO soccer_training (
			ts, game_id, league, home_team, away_team, norm_home, norm_away,
			half, event_type, home_score, away_score, time_remain,
			red_cards_home, red_cards_away,
			pregame_home_pct, pregame_draw_pct, pregame_away_pct, pregame_g0,
			actual_outcome
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.Ts.UTC().Format(time.RFC3339Nano),
		row.GameID,
		row.League,
		row.HomeTeam,
		row.AwayTeam,
		row.NormHome,
		row.NormAway,
		row.Half,
		row.EventType,
		row.HomeScore,
		row.AwayScore,
		row.TimeRemain,
		row.RedCardsHome,
		row.RedCardsAway,
		round5(row.PregameHomePct),
		round5(row.PregameDrawPct),
		round5(row.PregameAwayPct),
		round5(row.PregameG0),
		row.ActualOutcome,
	)
	if err != nil {
		return 0, fmt.Errorf("soccer training insert: %w", err)
	}

	id, _ := res.LastInsertId()
	s.rowCount++

	// Refresh cached size periodically and check eviction.
	if s.rowCount%100 == 0 {
		s.refreshSize()
		if s.cachedSize > maxStoreBytes {
			s.evict()
		}
	}

	return id, nil
}

// BackfillOdds updates the odds columns for a previously inserted row.
// Runs asynchronously — errors are logged but not propagated.
func (s *Store) BackfillOdds(rowID int64, odds OddsBackfill) {
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		_, err := s.db.Exec(
			`UPDATE soccer_training SET
				pinnacle_home_pct_l = ?,
				pinnacle_draw_pct_l = ?,
				pinnacle_away_pct_l = ?,
				kalshi_home_pct_l   = ?,
				kalshi_draw_pct_l   = ?,
				kalshi_away_pct_l   = ?
			WHERE id = ?`,
		round5(odds.PinnacleHomePctL),
		round5(odds.PinnacleDrawPctL),
		round5(odds.PinnacleAwayPctL),
		round5(odds.KalshiHomePctL),
		round5(odds.KalshiDrawPctL),
		round5(odds.KalshiAwayPctL),
			rowID,
		)
		if err != nil {
			telemetry.Warnf("soccer training backfill (row %d): %v", rowID, err)
		}
	}()
}

// refreshSize re-reads the database file size from SQLite pragmas.
// Must be called with s.mu held.
func (s *Store) refreshSize() {
	var size int64
	row := s.db.QueryRow(`SELECT COALESCE(page_count * page_size, 0) FROM pragma_page_count(), pragma_page_size()`)
	if err := row.Scan(&size); err == nil {
		s.cachedSize = size
	}
}

// evict deletes the oldest 10% of rows by count.
// Must be called with s.mu held.
func (s *Store) evict() {
	toDelete := int64(float64(s.rowCount) * evictPct)
	if toDelete < 1 {
		toDelete = 1
	}

	res, err := s.db.Exec(
		`DELETE FROM soccer_training WHERE id IN (
			SELECT id FROM soccer_training ORDER BY id ASC LIMIT ?
		)`, toDelete,
	)
	if err != nil {
		telemetry.Warnf("soccer training evict: %v", err)
		return
	}

	deleted, _ := res.RowsAffected()
	s.rowCount -= deleted
	s.evictCounter++

	telemetry.Infof("soccer training: evicted %d rows (target %d)", deleted, toDelete)

	if s.evictCounter%vacuumInterval == 0 {
		s.db.Exec(`PRAGMA incremental_vacuum`)
	}

	s.refreshSize()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
