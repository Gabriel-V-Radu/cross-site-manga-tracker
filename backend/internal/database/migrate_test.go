package database

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// baselineFile is the first embedded migration since the 2026-11 squash.
const baselineFile = "0001_baseline_2026-11.sql"

// laterFiles are the migrations added after the squash, in order.
var laterFiles = []string{
	"0002_series_metadata.sql",
}

// expectCurrentLedger fails unless the ledger names exactly the baseline and
// every later migration.
func expectCurrentLedger(t *testing.T, applied map[string]bool) {
	t.Helper()
	if !applied[baselineFile] || len(applied) != 1+len(laterFiles) {
		t.Fatalf("expected the baseline and %v in the ledger, got %v", laterFiles, applied)
	}
	for _, name := range laterFiles {
		if !applied[name] {
			t.Fatalf("expected %s in the ledger, got %v", name, applied)
		}
	}
}

// squashedFiles are the ledger names the baseline replaced, as a database
// deployed before the squash lists them.
var squashedFiles = []string{
	"0001_baseline.sql",
	"0002_source_logos_shared.sql",
	"0003_prune_history.sql",
	"0004_poll_rotation.sql",
}

func openMigrateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func ledger(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	versions := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		versions[v] = true
	}
	return versions
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
		t.Fatalf("look up column %s.%s: %v", table, column, err)
	}
	return count == 1
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("look up table %s: %v", name, err)
	}
	return count == 1
}

// schemaSignature describes every table's columns and foreign keys and every
// index, independent of the SQL text that created them: a table ALTERed into
// shape and one created whole read the same.
func schemaSignature(t *testing.T, db *sql.DB) string {
	t.Helper()
	var signature strings.Builder
	queries := []string{
		`SELECT m.name, p.cid, p.name, p.type, p."notnull", COALESCE(p.dflt_value, ''), p.pk
		 FROM sqlite_master m JOIN pragma_table_info(m.name) p
		 WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name, p.cid`,
		`SELECT m.name, f."table", f."from", f."to", f.on_delete
		 FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f
		 WHERE m.type = 'table' ORDER BY m.name, f.id, f.seq`,
		`SELECT name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' ORDER BY name`,
	}
	for _, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatalf("read schema: %v", err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatalf("scan schema: %v", err)
			}
			signature.WriteString(fmt.Sprintln(values...))
		}
		rows.Close()
	}
	return signature.String()
}

// A fresh database gets the baseline — the whole current schema, none of what
// earlier files created only to drop again — and a second run has nothing
// left to do.
func TestApplyMigrationsFreshDatabaseAndRerun(t *testing.T) {
	db := openMigrateTestDB(t)

	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	applied := ledger(t, db)
	expectCurrentLedger(t, applied)
	if !tableExists(t, db, "source_metadata") || !tableExists(t, db, "tracker_metadata") || !columnExists(t, db, "trackers", "latest_chapter_found_at") {
		t.Fatal("expected the series metadata tables and trackers.latest_chapter_found_at")
	}
	if tableExists(t, db, "profile_source_logos") || !tableExists(t, db, "source_logos") {
		t.Fatal("expected the shared logo table and not the per-profile one")
	}
	if columnExists(t, db, "sources", "base_url") || columnExists(t, db, "sources", "config_path") {
		t.Fatal("expected the never-used yaml connector columns to be gone")
	}
	if !columnExists(t, db, "tracker_sources", "last_polled_at") || !columnExists(t, db, "tracker_sources", "in_step") {
		t.Fatal("expected the poll rotation's columns on tracker_sources")
	}

	before := schemaSignature(t, db)
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("second apply must be a no-op, got: %v", err)
	}
	if got := len(ledger(t, db)); got != len(applied) {
		t.Fatalf("second apply changed the ledger: %d -> %d rows", len(applied), got)
	}
	if schemaSignature(t, db) != before {
		t.Fatal("second apply changed the schema")
	}
}

// A database deployed before the squash has the complete schema and a ledger
// naming the files the baseline replaced, not the baseline. The baseline must
// then run on top of that schema without changing it or its rows, and leave
// the ledger naming only itself.
func TestApplyMigrationsAdoptsADatabaseFromBeforeTheSquash(t *testing.T) {
	db := openMigrateTestDB(t)

	// Stand in for the squashed files: the baseline is exactly the schema
	// they produced, so applying it and then forgetting it did is the state
	// a deployed database is in.
	if err := ensureMigrationsTable(db); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(db, baselineFile); err != nil {
		t.Fatalf("build pre-squash schema: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	for _, name := range squashedFiles {
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO trackers (id, profile_id, title, source_id, source_url, status) VALUES (7, 1, 'Alive', 1, 'https://mangadex.org/title/alive', 'reading');
		INSERT INTO tracker_sources (tracker_id, source_id, source_url, last_polled_at, in_step) VALUES (7, 2, 'https://mangafire.to/title/alive', '2026-11-07 09:00:00', 1);
		INSERT INTO source_logos (source_id, logo_url) VALUES (1, '/uploads/site-logos/source-1.png');
	`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	before := schemaSignature(t, db)

	// The baseline alone first: it must leave a deployed schema as it found
	// it. The migrations after it change the schema on purpose.
	if err := applyMigration(db, baselineFile); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	applied := ledger(t, db)
	if !applied[baselineFile] || len(applied) != 1 {
		t.Fatalf("expected the squashed files' rows replaced by the baseline's, got %v", applied)
	}
	if after := schemaSignature(t, db); after != before {
		t.Fatalf("the baseline altered a deployed schema:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("apply the later migrations: %v", err)
	}
	expectCurrentLedger(t, ledger(t, db))
	var links, logos, inStep int
	if err := db.QueryRow(`SELECT (SELECT COUNT(1) FROM tracker_sources), (SELECT COUNT(1) FROM source_logos), (SELECT in_step FROM tracker_sources)`).Scan(&links, &logos, &inStep); err != nil {
		t.Fatal(err)
	}
	if links != 1 || logos != 1 || inStep != 1 {
		t.Fatalf("expected the deployed rows untouched, got %d links, %d logos, in_step=%d", links, logos, inStep)
	}
}
