package goalserve_ws

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

const (
	maxWSStoreBytes  int64 = 3 << 30 // 3 GiB
	wsEvictBatchSize       = 100
	wsVacuumInterval       = 50
)

// Store persists raw GoalServe WebSocket messages in a FIFO SQLite database
// capped at ~3 GiB. Oldest rows are evicted when the budget is exceeded.
type Store struct {
	db           *sql.DB
	mu           sync.Mutex
	cachedSize   int64
	evictCounter int
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create ws store dir: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)

	var avMode int
	if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&avMode); err != nil {
		db.Close()
		return nil, fmt.Errorf("read auto_vacuum: %w", err)
	}
	if avMode != 2 { // 2 = INCREMENTAL
		telemetry.Plainf("ws store: auto_vacuum=%d, switching to INCREMENTAL via full VACUUM", avMode)
		if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			db.Close()
			return nil, fmt.Errorf("set auto_vacuum: %w", err)
		}
		if _, err := db.Exec(`VACUUM`); err != nil {
			telemetry.Warnf("ws store: VACUUM to enable auto_vacuum failed: %v", err)
		}
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS ws_payloads (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			sport     TEXT    NOT NULL,
			msg_type  TEXT    NOT NULL,
			received  TEXT    NOT NULL,
			byte_size INTEGER NOT NULL,
			raw       BLOB    NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wsp_received ON ws_payloads(received)`,
		`CREATE INDEX IF NOT EXISTS idx_wsp_sport    ON ws_payloads(sport)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init ws schema (%s): %w", stmt, err)
		}
	}

	var size int64
	row := db.QueryRow(`SELECT COALESCE(SUM(byte_size), 0) FROM ws_payloads`)
	if err := row.Scan(&size); err != nil {
		db.Close()
		return nil, fmt.Errorf("read current ws size: %w", err)
	}

	telemetry.Plainf("ws store: opened %s  rows_bytes=%d", path, size)

	return &Store{db: db, cachedSize: size}, nil
}

// Insert stores a raw WS message asynchronously.
func (s *Store) Insert(sport, msgType string, raw []byte) {
	if s == nil {
		return
	}
	rawLen := int64(len(raw))
	rawCopy := make([]byte, rawLen)
	copy(rawCopy, raw)

	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		_, err := s.db.Exec(
			`INSERT INTO ws_payloads (sport, msg_type, received, byte_size, raw) VALUES (?, ?, ?, ?, ?)`,
			sport,
			msgType,
			time.Now().UTC().Format(time.RFC3339Nano),
			rawLen,
			rawCopy,
		)
		if err != nil {
			telemetry.Warnf("ws store: insert failed: %v", err)
			return
		}

		s.cachedSize += rawLen
		if s.cachedSize > maxWSStoreBytes {
			s.evict()
		}
	}()
}

func (s *Store) evict() {
	for s.cachedSize > maxWSStoreBytes {
		var freed int64
		err := s.db.QueryRow(
			`WITH deleted AS (
				DELETE FROM ws_payloads
				WHERE id IN (SELECT id FROM ws_payloads ORDER BY id ASC LIMIT ?)
				RETURNING byte_size
			)
			SELECT COALESCE(SUM(byte_size), 0) FROM deleted`,
			wsEvictBatchSize,
		).Scan(&freed)
		if err != nil {
			telemetry.Warnf("ws store: eviction query failed: %v", err)
			break
		}
		if freed == 0 {
			telemetry.Warnf("ws store: eviction freed 0 bytes, cachedSize=%d", s.cachedSize)
			break
		}
		s.cachedSize -= freed
		s.evictCounter++

		if s.evictCounter%wsVacuumInterval == 0 {
			if _, err := s.db.Exec(`PRAGMA incremental_vacuum`); err != nil {
				telemetry.Warnf("ws store: incremental_vacuum failed: %v", err)
			}
		}
	}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
