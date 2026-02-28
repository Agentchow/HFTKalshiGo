package tracking

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
	maxStoreBytes  int64   = 1 << 30 // 1 GiB
	evictPct       float64 = 0.10    // evict oldest 10% of rows
	vacuumInterval         = 10      // incremental vacuum every N evictions
)

// Store persists BatchOrderContext records in a FIFO SQLite database
// capped at ~1 GiB. Oldest 10% of rows are evicted when the budget is exceeded.
type Store struct {
	db           *sql.DB
	mu           sync.Mutex
	cachedSize   int64
	rowCount     int64
	evictCounter int
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create tracking store dir: %w", err)
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
	if avMode != 2 {
		if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			db.Close()
			return nil, fmt.Errorf("set auto_vacuum: %w", err)
		}
		if _, err := db.Exec(`VACUUM`); err != nil {
			telemetry.Warnf("tracking store: VACUUM to enable auto_vacuum failed: %v", err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init tracking schema: %w", err)
	}

	for _, col := range []string{"home_order_id", "away_order_id", "draw_order_id"} {
		db.Exec(fmt.Sprintf(`ALTER TABLE batch_orders ADD COLUMN %s TEXT`, col))
	}
	for _, col := range []string{
		"home_yes_order_id TEXT", "home_yes_ticker TEXT", "home_yes_limit_cents INTEGER",
		"home_yes_cost_cents INTEGER", "home_yes_fill_count INTEGER", "home_yes_total_count INTEGER",
		"home_no_order_id TEXT", "home_no_ticker TEXT", "home_no_limit_cents INTEGER",
		"home_no_cost_cents INTEGER", "home_no_fill_count INTEGER", "home_no_total_count INTEGER",
		"away_yes_order_id TEXT", "away_yes_ticker TEXT", "away_yes_limit_cents INTEGER",
		"away_yes_cost_cents INTEGER", "away_yes_fill_count INTEGER", "away_yes_total_count INTEGER",
		"away_no_order_id TEXT", "away_no_ticker TEXT", "away_no_limit_cents INTEGER",
		"away_no_cost_cents INTEGER", "away_no_fill_count INTEGER", "away_no_total_count INTEGER",
		"draw_yes_order_id TEXT", "draw_yes_ticker TEXT", "draw_yes_limit_cents INTEGER",
		"draw_yes_cost_cents INTEGER", "draw_yes_fill_count INTEGER", "draw_yes_total_count INTEGER",
		"draw_no_order_id TEXT", "draw_no_ticker TEXT", "draw_no_limit_cents INTEGER",
		"draw_no_cost_cents INTEGER", "draw_no_fill_count INTEGER", "draw_no_total_count INTEGER",
	} {
		db.Exec(fmt.Sprintf(`ALTER TABLE batch_orders ADD COLUMN %s`, col))
	}

	var size int64
	db.QueryRow(`SELECT COALESCE(page_count * page_size, 0) FROM pragma_page_count(), pragma_page_size()`).Scan(&size)
	var rowCount int64
	db.QueryRow(`SELECT COUNT(*) FROM batch_orders`).Scan(&rowCount)

	telemetry.Plainf("tracking store: opened %s  size=%d  rows=%d", path, size, rowCount)
	return &Store{db: db, cachedSize: size, rowCount: rowCount}, nil
}

const schema = `CREATE TABLE IF NOT EXISTS batch_orders (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	eid             TEXT    NOT NULL,
	sport           TEXT    NOT NULL,
	league          TEXT    NOT NULL,
	home_team       TEXT    NOT NULL,
	away_team       TEXT    NOT NULL,
	order_type      TEXT    NOT NULL,
	placed_at       TEXT    NOT NULL,

	home_score      INTEGER NOT NULL,
	away_score      INTEGER NOT NULL,
	period          TEXT    NOT NULL DEFAULT '',
	time_left       TEXT    NOT NULL DEFAULT '',

	-- Home outcome order (nullable group)
	home_order_id    TEXT,
	home_ticker      TEXT,
	home_side        TEXT,
	home_limit_cents INTEGER,
	home_cost_cents  INTEGER,
	home_fill_count  INTEGER,
	home_total_count INTEGER,

	-- Away outcome order (nullable group)
	away_order_id    TEXT,
	away_ticker      TEXT,
	away_side        TEXT,
	away_limit_cents INTEGER,
	away_cost_cents  INTEGER,
	away_fill_count  INTEGER,
	away_total_count INTEGER,

	-- Draw outcome order (nullable group, soccer only)
	draw_order_id    TEXT,
	draw_ticker      TEXT,
	draw_side        TEXT,
	draw_limit_cents INTEGER,
	draw_cost_cents  INTEGER,
	draw_fill_count  INTEGER,
	draw_total_count INTEGER,

	-- Follow-up prices
	price_1s_home_yes_ask  REAL,
	price_1s_away_yes_ask  REAL,
	price_1s_draw_yes_ask  REAL,
	price_5s_home_yes_ask  REAL,
	price_5s_away_yes_ask  REAL,
	price_5s_draw_yes_ask  REAL,
	price_10s_home_yes_ask REAL,
	price_10s_away_yes_ask REAL,
	price_10s_draw_yes_ask REAL,

	-- Settlement
	final_outcome TEXT,
	final_pnl     INTEGER
)`

// InsertBatch stores a new batch order record and returns the row ID.
func (s *Store) InsertBatch(b *BatchOrderContext) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(
		`INSERT INTO batch_orders (
			eid, sport, league, home_team, away_team, order_type, placed_at,
			home_score, away_score, period, time_left,
			home_yes_order_id, home_yes_ticker, home_yes_limit_cents, home_yes_cost_cents, home_yes_fill_count, home_yes_total_count,
			home_no_order_id,  home_no_ticker,  home_no_limit_cents,  home_no_cost_cents,  home_no_fill_count,  home_no_total_count,
			away_yes_order_id, away_yes_ticker, away_yes_limit_cents, away_yes_cost_cents, away_yes_fill_count, away_yes_total_count,
			away_no_order_id,  away_no_ticker,  away_no_limit_cents,  away_no_cost_cents,  away_no_fill_count,  away_no_total_count,
			draw_yes_order_id, draw_yes_ticker, draw_yes_limit_cents, draw_yes_cost_cents, draw_yes_fill_count, draw_yes_total_count,
			draw_no_order_id,  draw_no_ticker,  draw_no_limit_cents,  draw_no_cost_cents,  draw_no_fill_count,  draw_no_total_count
		) VALUES (?,?,?,?,?,?,?, ?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?)`,
		b.GameEID, b.Sport, b.League, b.HomeTeam, b.AwayTeam, b.OrderType,
		b.PlacedAt.UTC().Format(time.RFC3339Nano),
		b.HomeScore, b.AwayScore, b.Period, b.TimeLeft,
		ooStr(b.HomeYes, func(o *OutcomeOrder) string { return o.OrderID }),
		ooStr(b.HomeYes, func(o *OutcomeOrder) string { return o.Ticker }),
		ooInt(b.HomeYes, func(o *OutcomeOrder) int { return o.LimitCents }),
		ooInt(b.HomeYes, func(o *OutcomeOrder) int { return o.CostCents }),
		ooInt(b.HomeYes, func(o *OutcomeOrder) int { return o.FillCount }),
		ooInt(b.HomeYes, func(o *OutcomeOrder) int { return o.TotalCount }),
		ooStr(b.HomeNo, func(o *OutcomeOrder) string { return o.OrderID }),
		ooStr(b.HomeNo, func(o *OutcomeOrder) string { return o.Ticker }),
		ooInt(b.HomeNo, func(o *OutcomeOrder) int { return o.LimitCents }),
		ooInt(b.HomeNo, func(o *OutcomeOrder) int { return o.CostCents }),
		ooInt(b.HomeNo, func(o *OutcomeOrder) int { return o.FillCount }),
		ooInt(b.HomeNo, func(o *OutcomeOrder) int { return o.TotalCount }),
		ooStr(b.AwayYes, func(o *OutcomeOrder) string { return o.OrderID }),
		ooStr(b.AwayYes, func(o *OutcomeOrder) string { return o.Ticker }),
		ooInt(b.AwayYes, func(o *OutcomeOrder) int { return o.LimitCents }),
		ooInt(b.AwayYes, func(o *OutcomeOrder) int { return o.CostCents }),
		ooInt(b.AwayYes, func(o *OutcomeOrder) int { return o.FillCount }),
		ooInt(b.AwayYes, func(o *OutcomeOrder) int { return o.TotalCount }),
		ooStr(b.AwayNo, func(o *OutcomeOrder) string { return o.OrderID }),
		ooStr(b.AwayNo, func(o *OutcomeOrder) string { return o.Ticker }),
		ooInt(b.AwayNo, func(o *OutcomeOrder) int { return o.LimitCents }),
		ooInt(b.AwayNo, func(o *OutcomeOrder) int { return o.CostCents }),
		ooInt(b.AwayNo, func(o *OutcomeOrder) int { return o.FillCount }),
		ooInt(b.AwayNo, func(o *OutcomeOrder) int { return o.TotalCount }),
		ooStr(b.DrawYes, func(o *OutcomeOrder) string { return o.OrderID }),
		ooStr(b.DrawYes, func(o *OutcomeOrder) string { return o.Ticker }),
		ooInt(b.DrawYes, func(o *OutcomeOrder) int { return o.LimitCents }),
		ooInt(b.DrawYes, func(o *OutcomeOrder) int { return o.CostCents }),
		ooInt(b.DrawYes, func(o *OutcomeOrder) int { return o.FillCount }),
		ooInt(b.DrawYes, func(o *OutcomeOrder) int { return o.TotalCount }),
		ooStr(b.DrawNo, func(o *OutcomeOrder) string { return o.OrderID }),
		ooStr(b.DrawNo, func(o *OutcomeOrder) string { return o.Ticker }),
		ooInt(b.DrawNo, func(o *OutcomeOrder) int { return o.LimitCents }),
		ooInt(b.DrawNo, func(o *OutcomeOrder) int { return o.CostCents }),
		ooInt(b.DrawNo, func(o *OutcomeOrder) int { return o.FillCount }),
		ooInt(b.DrawNo, func(o *OutcomeOrder) int { return o.TotalCount }),
	)
	if err != nil {
		return 0, fmt.Errorf("insert batch order: %w", err)
	}

	id, _ := res.LastInsertId()
	s.rowCount++
	s.refreshSize()
	if s.cachedSize > maxStoreBytes {
		s.evict()
	}
	return id, nil
}

// UpdateFollowUpPrices sets the price snapshot columns for a given checkpoint.
func (s *Store) UpdateFollowUpPrices(rowID int64, checkpoint string, snap PriceSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var query string
	switch checkpoint {
	case "1s":
		query = `UPDATE batch_orders SET price_1s_home_yes_ask=?, price_1s_away_yes_ask=?, price_1s_draw_yes_ask=? WHERE id=?`
	case "5s":
		query = `UPDATE batch_orders SET price_5s_home_yes_ask=?, price_5s_away_yes_ask=?, price_5s_draw_yes_ask=? WHERE id=?`
	case "10s":
		query = `UPDATE batch_orders SET price_10s_home_yes_ask=?, price_10s_away_yes_ask=?, price_10s_draw_yes_ask=? WHERE id=?`
	default:
		return
	}

	if _, err := s.db.Exec(query, snap.HomeYesAsk, snap.AwayYesAsk, snap.DrawYesAsk, rowID); err != nil {
		telemetry.Warnf("tracking: update %s prices (row %d): %v", checkpoint, rowID, err)
	}
}

// UpdateFinalFill overwrites the cost/fill columns for one outcome leg
// after polling the Kalshi GetOrder endpoint for final fill status.
func (s *Store) UpdateFinalFill(rowID int64, outcomeKey, orderID string, costCents, fillCount, totalCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	colMap := map[string]string{
		"home_yes": "home_yes", "home_no": "home_no",
		"away_yes": "away_yes", "away_no": "away_no",
		"draw_yes": "draw_yes", "draw_no": "draw_no",
	}
	prefix, ok := colMap[outcomeKey]
	if !ok {
		return
	}

	query := fmt.Sprintf(`UPDATE batch_orders SET %s_order_id=?, %s_cost_cents=?, %s_fill_count=?, %s_total_count=? WHERE id=?`,
		prefix, prefix, prefix, prefix)

	if _, err := s.db.Exec(query, orderID, costCents, fillCount, totalCount, rowID); err != nil {
		telemetry.Warnf("tracking: update final fill %s (row %d): %v", outcomeKey, rowID, err)
	}
}

// batchRow is the subset of columns needed for settlement P&L calculation.
type batchRow struct {
	ID           int64
	HomeYesCost  sql.NullInt64
	HomeNoCost   sql.NullInt64
	AwayYesCost  sql.NullInt64
	AwayNoCost   sql.NullInt64
	DrawYesCost  sql.NullInt64
	DrawNoCost   sql.NullInt64
}

// UnsettledForEID returns all batch orders for a game that lack a final outcome.
func (s *Store) UnsettledForEID(eid string) ([]batchRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT id,
			home_yes_cost_cents, home_no_cost_cents,
			away_yes_cost_cents, away_no_cost_cents,
			draw_yes_cost_cents, draw_no_cost_cents
		 FROM batch_orders WHERE eid = ? AND final_outcome IS NULL`, eid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []batchRow
	for rows.Next() {
		var r batchRow
		if err := rows.Scan(&r.ID,
			&r.HomeYesCost, &r.HomeNoCost,
			&r.AwayYesCost, &r.AwayNoCost,
			&r.DrawYesCost, &r.DrawNoCost,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateSettlement sets the final outcome and P&L for a batch order row.
func (s *Store) UpdateSettlement(rowID int64, outcome string, pnlCents int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(
		`UPDATE batch_orders SET final_outcome=?, final_pnl=? WHERE id=?`,
		outcome, pnlCents, rowID,
	); err != nil {
		telemetry.Warnf("tracking: update settlement (row %d): %v", rowID, err)
	}
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
		`DELETE FROM batch_orders WHERE id IN (
			SELECT id FROM batch_orders ORDER BY id ASC LIMIT ?
		)`, toDelete,
	)
	if err != nil {
		telemetry.Warnf("tracking store evict: %v", err)
		return
	}

	deleted, _ := res.RowsAffected()
	s.rowCount -= deleted
	s.evictCounter++

	telemetry.Infof("tracking store: evicted %d rows (target %d)", deleted, toDelete)

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

func ooStr(o *OutcomeOrder, fn func(*OutcomeOrder) string) any {
	if o == nil {
		return nil
	}
	return fn(o)
}

func ooInt(o *OutcomeOrder, fn func(*OutcomeOrder) int) any {
	if o == nil {
		return nil
	}
	return fn(o)
}
