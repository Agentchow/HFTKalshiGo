package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	team := flag.String("team", "", "substring to search for in raw payload (case-insensitive)")
	n := flag.Int("n", 10, "max results to return")
	sport := flag.String("sport", "", "filter by sport (hockey, soccer, etc.)")
	pretty := flag.Bool("pretty", false, "pretty-print JSON")
	dbPath := flag.String("db", "data/goalserve_ws.db", "path to WS store")
	flag.Parse()

	if *team == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/inspect_ws -team <name> [-n 10] [-sport hockey] [-pretty]")
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", *dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	q := `SELECT id, sport, msg_type, received, byte_size, raw FROM ws_payloads WHERE CAST(raw AS TEXT) LIKE ?`
	args := []any{"%" + *team + "%"}
	if *sport != "" {
		q += ` AND sport = ?`
		args = append(args, *sport)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, *n)

	rows, err := db.Query(q, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id int64
		var sportVal, msgType, received string
		var byteSize int
		var raw []byte
		if err := rows.Scan(&id, &sportVal, &msgType, &received, &byteSize, &raw); err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			continue
		}
		count++

		rawStr := string(raw)
		if *pretty {
			var buf bytes.Buffer
			if err := json.Indent(&buf, raw, "", "  "); err == nil {
				rawStr = buf.String()
			}
		}

		fmt.Printf("--- id=%d sport=%s type=%s received=%s bytes=%d ---\n%s\n\n", id, sportVal, msgType, received, byteSize, rawStr)
	}
	if count == 0 {
		fmt.Printf("(no payloads matching %q found)\n", *team)
	} else {
		fmt.Printf("(%d results)\n", count)
	}
}
