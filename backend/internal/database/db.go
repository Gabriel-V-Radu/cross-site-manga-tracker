package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// TIMESTAMP COLUMNS HOLD TWO DIFFERENT SPELLINGS. Read this before writing a
// query that does date arithmetic in SQL.
//
// The DSN is a bare path, so modernc.org/sqlite binds a time.Time with
// time.Time.String() — "2026-08-29 14:03:22.123456789 +0000 UTC". SQLite's own
// CURRENT_TIMESTAMP, which the schema uses for column defaults, writes
// "2026-08-29 14:03:22". Both land in the same columns (created_at,
// updated_at, last_checked_at, latest_release_at, ...), and which one a row
// carries depends only on whether Go or a DEFAULT wrote it.
//
// This works today for exactly two reasons: the driver parses both spellings
// back into time.Time on read, and the first nineteen characters are the same
// layout, so ORDER BY and text comparison (>=, BETWEEN, MIN/MAX) rank them
// correctly.
//
// What does NOT work is SQLite's date functions. datetime(), strftime(),
// date() and julianday() return NULL for the driver's spelling — the
// " +0000 UTC" tail is not a time string SQLite recognizes — so a predicate
// like `datetime(last_checked_at) < datetime('now','-1 day')` silently drops
// every Go-written row instead of failing. Nothing calls them today; keep it
// that way. Compare the columns as text against a value formatted the same
// way, or read the timestamp into Go and do the arithmetic there.
//
// Do not try to normalize the stored data: rewriting live timestamp columns
// risks far more than the queries it would enable.
func Open(sqlitePath string) (*sql.DB, error) {
	dir := filepath.Dir(sqlitePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite dir: %w", err)
	}

	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// One connection, not a pool. PRAGMAs issued through database/sql apply
	// to whichever pooled connection runs them, so with a pool the settings
	// below only ever covered one connection — and two of our own connections
	// could contend hard enough to surface SQLITE_BUSY (a link scan writing
	// while the dashboard read). SQLite serializes writers anyway; a single
	// connection makes the serialization explicit and the PRAGMAs reliable.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite WAL: %w", err)
	}

	// Still worth having with one connection: external processes (backups,
	// the CLI tools) can hold the file, and waiting beats failing.
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return db, nil
}
