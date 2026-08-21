package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

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
