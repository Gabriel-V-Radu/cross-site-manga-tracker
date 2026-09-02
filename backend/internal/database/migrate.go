package database

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// The schema ships inside the binary. It used to be read from a directory at
// runtime (MIGRATIONS_PATH), which meant the image had to copy the files next
// to the binary, the compose file had to say where, and every test had to
// compute a relative path to them; a binary that carries its own schema has
// none of those.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationsDir = "migrations"

// ApplyMigrations runs every embedded migration the ledger does not list yet,
// in filename order, each in its own transaction. The ledger keys on the bare
// filename, so a file is applied once per database and must never be renamed.
//
// A database that predates the baseline squash has a ledger naming the thirty
// historical files and not 0001_baseline.sql, so the baseline runs there on
// top of a complete schema. It is written to be a no-op in that case (IF NOT
// EXISTS throughout, no rows), which is the property any future squash has to
// keep as well.
func ApplyMigrations(db *sql.DB) error {
	if err := ensureMigrationsTable(db); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrationFiles, migrationsDir)
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	fileNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			fileNames = append(fileNames, entry.Name())
		}
	}
	sort.Strings(fileNames)

	for _, fileName := range fileNames {
		applied, err := migrationApplied(db, fileName)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(db, fileName); err != nil {
			return err
		}
	}

	return nil
}

// applyMigration runs one embedded file and records it, in one transaction.
func applyMigration(db *sql.DB, fileName string) error {
	content, err := migrationFiles.ReadFile(migrationsDir + "/" + fileName)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", fileName, err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}

	if _, err := tx.Exec(string(content)); err != nil {
		tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", fileName, err)
	}

	if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, fileName); err != nil {
		tx.Rollback()
		return fmt.Errorf("record migration %s: %w", fileName, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", fileName, err)
	}
	return nil
}

func ensureMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}
	return nil
}

func migrationApplied(db *sql.DB, version string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}
	return count > 0, nil
}
