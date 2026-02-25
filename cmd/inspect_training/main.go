package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	_ "modernc.org/sqlite"
)

var soccerCompact = `SELECT id, home_team, away_team, event_type,
	home_score||'-'||away_score AS score, half,
	CAST(time_remain AS INT) AS min_left,
	red_cards_home AS rc_h, red_cards_away AS rc_a,
	printf('%.1f', bet365_home_pct_l*100) AS b365_h,
	printf('%.1f', bet365_draw_pct_l*100) AS b365_d,
	printf('%.1f', bet365_away_pct_l*100) AS b365_a,
	printf('%.0f', kalshi_home_pct_l*100) AS kal_h,
	printf('%.0f', kalshi_draw_pct_l*100) AS kal_d,
	printf('%.0f', kalshi_away_pct_l*100) AS kal_a,
	COALESCE(actual_outcome,'') AS outcome
FROM soccer_training ORDER BY id DESC LIMIT ?`

var hockeyCompact = `SELECT id, home_team, away_team, event_type,
	home_score||'-'||away_score AS score, period,
	CAST(time_remain AS INT) AS min_left,
	home_power_play AS pp_h, away_power_play AS pp_a,
	printf('%.1f', bet365_home_pct_l*100) AS b365_h,
	printf('%.1f', bet365_away_pct_l*100) AS b365_a,
	printf('%.0f', kalshi_home_pct_l*100) AS kal_h,
	printf('%.0f', kalshi_away_pct_l*100) AS kal_a,
	COALESCE(actual_outcome,'') AS outcome
FROM training_snapshots ORDER BY id DESC LIMIT ?`

func main() {
	n := flag.Int("n", 10, "number of recent rows to display")
	sport := flag.String("sport", "all", "which DB to inspect: soccer, hockey, or all")
	verbose := flag.Bool("v", false, "show all columns (raw schema)")
	flag.Parse()

	if *sport != "soccer" && *sport != "hockey" && *sport != "all" {
		fmt.Fprintf(os.Stderr, "unknown sport %q (use soccer, hockey, or all)\n", *sport)
		os.Exit(1)
	}

	switch *sport {
	case "soccer", "all":
		if *verbose {
			printRaw("Soccer Training", "data/soccer_training.db", "soccer_training", *n)
		} else {
			printCompact("Soccer Training", "data/soccer_training.db", "soccer_training", soccerCompact, *n)
		}
	}
	if *sport == "all" {
		fmt.Println()
	}
	switch *sport {
	case "hockey", "all":
		if *verbose {
			printRaw("Hockey Training", "data/hockey_training.db", "training_snapshots", *n)
		} else {
			printCompact("Hockey Training", "data/hockey_training.db", "training_snapshots", hockeyCompact, *n)
		}
	}
}

func printCompact(title, dbPath, table, query string, n int) {
	fmt.Printf("=== %s ===\n", title)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Printf("  (cannot open %s: %v)\n", dbPath, err)
		return
	}
	defer db.Close()

	count := 0
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		fmt.Printf("  (cannot count rows: %v)\n", err)
		return
	}
	if count == 0 {
		fmt.Println("(no data)")
		return
	}

	fmt.Printf("Rows: %d  |  Showing last %d:\n", count, min(n, count))
	printQuery(db, query, n)
}

func printRaw(title, dbPath, table string, n int) {
	fmt.Printf("=== %s (verbose) ===\n", title)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Printf("  (cannot open %s: %v)\n", dbPath, err)
		return
	}
	defer db.Close()

	cols, err := schemaColumns(db, table)
	if err != nil {
		fmt.Printf("  (cannot read schema: %v)\n", err)
		return
	}
	fmt.Printf("Schema: %s\n\n", strings.Join(cols, ", "))

	count := 0
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		fmt.Printf("  (cannot count rows: %v)\n", err)
		return
	}
	if count == 0 {
		fmt.Println("(no data)")
		return
	}

	fmt.Printf("Rows: %d  |  Showing last %d:\n", count, min(n, count))
	printQuery(db, fmt.Sprintf("SELECT * FROM %s ORDER BY id DESC LIMIT ?", table), n)
}

func printQuery(db *sql.DB, query string, n int) {
	rows, err := db.Query(query, n)
	if err != nil {
		fmt.Printf("  (query error: %v)\n", err)
		return
	}
	defer rows.Close()

	colNames, _ := rows.Columns()
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(colNames, "\t"))
	fmt.Fprintln(w, strings.Repeat("----\t", len(colNames)))

	vals := make([]any, len(colNames))
	ptrs := make([]any, len(colNames))
	for i := range vals {
		ptrs[i] = &vals[i]
	}

	var rowBuf [][]string
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			fmt.Fprintf(os.Stderr, "  scan error: %v\n", err)
			continue
		}
		cells := make([]string, len(colNames))
		for i, v := range vals {
			cells[i] = fmtCell(v)
		}
		rowBuf = append(rowBuf, cells)
	}

	for i := len(rowBuf) - 1; i >= 0; i-- {
		fmt.Fprintln(w, strings.Join(rowBuf[i], "\t"))
	}
	w.Flush()
}

func schemaColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, fmt.Sprintf("%s %s", name, ctype))
	}
	return cols, nil
}

func fmtCell(v any) string {
	if v == nil {
		return "-"
	}
	switch x := v.(type) {
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%.5f", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprintf("%v", v)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
