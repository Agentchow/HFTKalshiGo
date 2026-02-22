package goalserve_webhook

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"

	_ "modernc.org/sqlite"
)

const (
	maxStoreBytes    int64 = 1 << 30 // 1 GiB
	evictBatchSize         = 50
	vacuumInterval         = 100 // run incremental vacuum every N evictions
)

// Store persists raw gzip-compressed webhook payloads in a FIFO SQLite database
// capped at ~1 GiB of compressed data. Oldest rows are evicted when the budget is exceeded.
type Store struct {
	db           *sql.DB
	mu           sync.Mutex
	cachedSize   int64
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
		`CREATE TABLE IF NOT EXISTS webhook_payloads (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			sport     TEXT    NOT NULL,
			received  TEXT    NOT NULL,
			byte_size INTEGER NOT NULL,
			raw_gz    BLOB    NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wp_received ON webhook_payloads(received)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init schema (%s): %w", stmt, err)
		}
	}

	var size int64
	row := db.QueryRow(`SELECT COALESCE(SUM(byte_size), 0) FROM webhook_payloads`)
	if err := row.Scan(&size); err != nil {
		db.Close()
		return nil, fmt.Errorf("read current size: %w", err)
	}

	telemetry.Infof("webhook store: opened %s  rows_bytes=%d", path, size)

	return &Store{db: db, cachedSize: size}, nil
}

// Insert stores a raw gzip-compressed payload asynchronously.
func (s *Store) Insert(sport events.Sport, raw []byte) {
	rawLen := int64(len(raw))
	rawCopy := make([]byte, rawLen)
	copy(rawCopy, raw)

	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		_, err := s.db.Exec(
			`INSERT INTO webhook_payloads (sport, received, byte_size, raw_gz) VALUES (?, ?, ?, ?)`,
			string(sport),
			time.Now().UTC().Format(time.RFC3339Nano),
			rawLen,
			rawCopy,
		)
		if err != nil {
			telemetry.Warnf("webhook store: insert failed: %v", err)
			return
		}

		s.cachedSize += rawLen
		if s.cachedSize > maxStoreBytes {
			s.evict()
		}
	}()
}

// evict removes oldest rows until total size is under budget.
// Must be called with s.mu held.
func (s *Store) evict() {
	for s.cachedSize > maxStoreBytes {
		var freed int64
		err := s.db.QueryRow(
			`WITH deleted AS (
				DELETE FROM webhook_payloads
				WHERE id IN (SELECT id FROM webhook_payloads ORDER BY id ASC LIMIT ?)
				RETURNING byte_size
			)
			SELECT COALESCE(SUM(byte_size), 0) FROM deleted`,
			evictBatchSize,
		).Scan(&freed)
		if err != nil || freed == 0 {
			break
		}
		s.cachedSize -= freed
		s.evictCounter++

		if s.evictCounter%vacuumInterval == 0 {
			s.db.Exec(`PRAGMA incremental_vacuum`)
		}
	}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
