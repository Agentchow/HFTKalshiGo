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
			home_order_id, home_ticker, home_side, home_limit_cents, home_cost_cents, home_fill_count, home_total_count,
			away_order_id, away_ticker, away_side, away_limit_cents, away_cost_cents, away_fill_count, away_total_count,
			draw_order_id, draw_ticker, draw_side, draw_limit_cents, draw_cost_cents, draw_fill_count, draw_total_count
		) VALUES (?,?,?,?,?,?,?, ?,?,?,?, ?,?,?,?,?,?,?, ?,?,?,?,?,?,?, ?,?,?,?,?,?,?)`,
		b.GameEID, b.Sport, b.League, b.HomeTeam, b.AwayTeam, b.OrderType,
		b.PlacedAt.UTC().Format(time.RFC3339Nano),
		b.HomeScore, b.AwayScore, b.Period, b.TimeLeft,
		outcomeStr(b.Home, func(o *OutcomeOrder) string { return o.OrderID }),
		outcomeStr(b.Home, func(o *OutcomeOrder) string { return o.Ticker }),
		outcomeStr(b.Home, func(o *OutcomeOrder) string { return o.Side }),
		outcomeInt(b.Home, func(o *OutcomeOrder) int { return o.LimitCents }),
		outcomeInt(b.Home, func(o *OutcomeOrder) int { return o.CostCents }),
		outcomeInt(b.Home, func(o *OutcomeOrder) int { return o.FillCount }),
		outcomeInt(b.Home, func(o *OutcomeOrder) int { return o.TotalCount }),
		outcomeStr(b.Away, func(o *OutcomeOrder) string { return o.OrderID }),
		outcomeStr(b.Away, func(o *OutcomeOrder) string { return o.Ticker }),
		outcomeStr(b.Away, func(o *OutcomeOrder) string { return o.Side }),
		outcomeInt(b.Away, func(o *OutcomeOrder) int { return o.LimitCents }),
		outcomeInt(b.Away, func(o *OutcomeOrder) int { return o.CostCents }),
		outcomeInt(b.Away, func(o *OutcomeOrder) int { return o.FillCount }),
		outcomeInt(b.Away, func(o *OutcomeOrder) int { return o.TotalCount }),
		outcomeStr(b.Draw, func(o *OutcomeOrder) string { return o.OrderID }),
		outcomeStr(b.Draw, func(o *OutcomeOrder) string { return o.Ticker }),
		outcomeStr(b.Draw, func(o *OutcomeOrder) string { return o.Side }),
		outcomeInt(b.Draw, func(o *OutcomeOrder) int { return o.LimitCents }),
		outcomeInt(b.Draw, func(o *OutcomeOrder) int { return o.CostCents }),
		outcomeInt(b.Draw, func(o *OutcomeOrder) int { return o.FillCount }),
		outcomeInt(b.Draw, func(o *OutcomeOrder) int { return o.TotalCount }),
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
func (s *Store) UpdateFinalFill(rowID int64, outcome, orderID string, costCents, fillCount, totalCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var query string
	switch outcome {
	case "home":
		query = `UPDATE batch_orders SET home_order_id=?, home_cost_cents=?, home_fill_count=?, home_total_count=? WHERE id=?`
	case "away":
		query = `UPDATE batch_orders SET away_order_id=?, away_cost_cents=?, away_fill_count=?, away_total_count=? WHERE id=?`
	case "draw":
		query = `UPDATE batch_orders SET draw_order_id=?, draw_cost_cents=?, draw_fill_count=?, draw_total_count=? WHERE id=?`
	default:
		return
	}

	if _, err := s.db.Exec(query, orderID, costCents, fillCount, totalCount, rowID); err != nil {
		telemetry.Warnf("tracking: update final fill %s (row %d): %v", outcome, rowID, err)
	}
}

// batchRow is the subset of columns needed for settlement P&L calculation.
type batchRow struct {
	ID            int64
	HomeCostCents sql.NullInt64
	HomeSide      sql.NullString
	AwayCostCents sql.NullInt64
	AwaySide      sql.NullString
	DrawCostCents sql.NullInt64
	DrawSide      sql.NullString
}

// UnsettledForEID returns all batch orders for a game that lack a final outcome.
func (s *Store) UnsettledForEID(eid string) ([]batchRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT id, home_cost_cents, home_side, away_cost_cents, away_side, draw_cost_cents, draw_side
		 FROM batch_orders WHERE eid = ? AND final_outcome IS NULL`, eid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []batchRow
	for rows.Next() {
		var r batchRow
		if err := rows.Scan(&r.ID, &r.HomeCostCents, &r.HomeSide, &r.AwayCostCents, &r.AwaySide, &r.DrawCostCents, &r.DrawSide); err != nil {
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

func outcomeStr(o *OutcomeOrder, fn func(*OutcomeOrder) string) any {
	if o == nil {
		return nil
	}
	return fn(o)
}

func outcomeInt(o *OutcomeOrder, fn func(*OutcomeOrder) int) any {
	if o == nil {
		return nil
	}
	return fn(o)
}
