package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

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

// A fresh database gets every embedded file, in order, and a second run has
// nothing left to do.
func TestApplyMigrationsFreshDatabaseAndRerun(t *testing.T) {
	db := openMigrateTestDB(t)

	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	applied := ledger(t, db)
	if !applied["0001_baseline.sql"] || !applied["0002_source_logos_shared.sql"] || !applied["0003_prune_history.sql"] {
		t.Fatalf("expected the embedded files in the ledger, got %v", applied)
	}
	if tableExists(t, db, "profile_source_logos") || !tableExists(t, db, "source_logos") {
		t.Fatal("expected the per-profile logo table to be replaced by the shared one")
	}
	if columnExists(t, db, "sources", "base_url") || columnExists(t, db, "sources", "config_path") {
		t.Fatal("expected the never-used yaml connector columns to be gone")
	}

	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("second apply must be a no-op, got: %v", err)
	}
	if got := len(ledger(t, db)); got != len(applied) {
		t.Fatalf("second apply changed the ledger: %d -> %d rows", len(applied), got)
	}
}

// A database that predates the squash has the whole schema and a ledger that
// names the thirty historical files, not the baseline. The baseline must then
// run on top of the complete schema without changing it, the logo migration
// must carry the existing per-profile rows over (lowest profile first where
// both had one), and the housekeeping must prune the legacy ledger rows and
// the link rows whose tracker is gone.
func TestApplyMigrationsUpgradesAPreSquashDatabase(t *testing.T) {
	db := openMigrateTestDB(t)

	// Stand in for the historical migrations: the baseline is exactly the
	// schema they produced, so applying it and then forgetting it did is the
	// state a deployed database is in.
	if err := ensureMigrationsTable(db); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(db, "0001_baseline.sql"); err != nil {
		t.Fatalf("build pre-squash schema: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"0001_init.sql", "0013_profile_source_logos.sql", "0030_drop_dead_schema.sql"} {
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, legacy); err != nil {
			t.Fatal(err)
		}
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO profile_source_logos (profile_id, source_id, logo_url) VALUES
			(1, 1, '/uploads/site-logos/profile-1-source-1.png'),
			(2, 1, '/uploads/site-logos/profile-2-source-1.png'),
			(2, 2, '/uploads/site-logos/profile-2-source-2.png')
	`); err != nil {
		t.Fatalf("seed per-profile logos: %v", err)
	}
	// An orphaned link and tag, the way a delete with foreign keys off leaves
	// them; a live tracker's link must survive next to them.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO trackers (id, profile_id, title, source_id, source_url, status) VALUES (7, 1, 'Alive', 1, 'https://mangadex.org/title/alive', 'reading');
		INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (7, 2, 'https://mangafire.to/manga/alive'), (999, 2, 'https://mangafire.to/manga/gone');
		INSERT INTO custom_tags (id, profile_id, name) VALUES (1, 1, 'Favorite');
		INSERT INTO tracker_tags (tracker_id, tag_id) VALUES (7, 1), (999, 1);
	`); err != nil {
		t.Fatalf("seed orphans: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	var trackersColumns int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('trackers')`).Scan(&trackersColumns); err != nil {
		t.Fatal(err)
	}

	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	applied := ledger(t, db)
	if applied["0001_init.sql"] || applied["0030_drop_dead_schema.sql"] {
		t.Fatalf("expected the legacy ledger rows pruned, got %v", applied)
	}
	if !applied["0001_baseline.sql"] || !applied["0002_source_logos_shared.sql"] || !applied["0003_prune_history.sql"] || len(applied) != 3 {
		t.Fatalf("expected exactly the embedded files in the ledger, got %v", applied)
	}
	var links, tags int
	if err := db.QueryRow(`SELECT (SELECT COUNT(1) FROM tracker_sources), (SELECT COUNT(1) FROM tracker_tags)`).Scan(&links, &tags); err != nil {
		t.Fatal(err)
	}
	if links != 1 || tags != 1 {
		t.Fatalf("expected only the live tracker's link and tag to survive, got %d links and %d tags", links, tags)
	}
	if columnExists(t, db, "sources", "base_url") {
		t.Fatal("expected sources.base_url dropped")
	}
	var after int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('trackers')`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != trackersColumns {
		t.Fatalf("baseline altered an existing table: trackers went from %d to %d columns", trackersColumns, after)
	}

	if tableExists(t, db, "profile_source_logos") {
		t.Fatal("per-profile logo table survived the upgrade")
	}
	logos := map[int64]string{}
	rows, err := db.Query(`SELECT source_id, logo_url FROM source_logos ORDER BY source_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			t.Fatal(err)
		}
		logos[id] = url
	}
	rows.Close()
	if len(logos) != 2 {
		t.Fatalf("expected one shared logo per source, got %v", logos)
	}
	if logos[1] != "/uploads/site-logos/profile-1-source-1.png" {
		t.Fatalf("profile 1's logo must win where both profiles had one, got %q", logos[1])
	}
	if logos[2] != "/uploads/site-logos/profile-2-source-2.png" {
		t.Fatalf("a logo only profile 2 had must carry over, got %q", logos[2])
	}
}
