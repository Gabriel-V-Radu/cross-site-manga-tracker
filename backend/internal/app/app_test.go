package app

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
)

func pointAtTempDatabase(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "app.sqlite")
	t.Setenv("SQLITE_PATH", dbPath)
	t.Setenv("LOG_LEVEL", "INFO")
	return dbPath
}

func countMigrations(t *testing.T, rt *Runtime) int {
	t.Helper()
	var count int
	if err := rt.DB.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&count); err != nil {
		// No table at all reads as zero migrations applied.
		return 0
	}
	return count
}

// A run that is going to write brings the schema up to date and seeds the
// defaults; the sources the connectors need exist afterwards.
func TestOpenMigratesAndSeedsWhenAsked(t *testing.T) {
	pointAtTempDatabase(t)

	rt, err := Open(Options{Migrate: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rt.Close()

	if got := countMigrations(t, rt); got == 0 {
		t.Fatal("expected migrations to be applied")
	}
	var sources int
	if err := rt.DB.QueryRow(`SELECT COUNT(1) FROM sources`).Scan(&sources); err != nil || sources == 0 {
		t.Fatalf("expected seeded sources, got %d (%v)", sources, err)
	}
}

// A preview must not advance the schema of the database it is pointed at: a
// dry run from a newer checkout than the running image would otherwise migrate
// the live database out from under it.
func TestOpenWithoutMigrateLeavesTheSchemaAlone(t *testing.T) {
	pointAtTempDatabase(t)

	rt, err := Open(Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rt.Close()

	if got := countMigrations(t, rt); got != 0 {
		t.Fatalf("a non-migrating open applied %d migrations", got)
	}
}

// statisticsRows reopens the database at path and counts the planner
// statistics it holds; a database nobody analyzed has no sqlite_stat1 at all.
func statisticsRows(t *testing.T, path string) int {
	t.Helper()
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_stat1`).Scan(&rows); err != nil {
		return 0
	}
	return rows
}

// The server leaves the query planner its statistics when it closes the
// database; a run that did not ask for that — every tool, and a preview above
// all — closes it without writing anything.
func TestCloseLeavesStatisticsOnlyWhenAsked(t *testing.T) {
	work := func(t *testing.T, rt *Runtime) {
		t.Helper()
		for index := 0; index < 50; index++ {
			if _, err := rt.DB.Exec(`
				INSERT INTO trackers (profile_id, title, source_id, source_url, status)
				VALUES (1, ?, 1, ?, 'reading')
			`, fmt.Sprintf("Series %d", index), fmt.Sprintf("https://mangadex.org/title/%d", index)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		// The dashboard's own question, through an index statistics would
		// inform.
		var reading int
		if err := rt.DB.QueryRow(`SELECT COUNT(1) FROM trackers WHERE profile_id = 1 AND status = 'reading'`).Scan(&reading); err != nil || reading != 50 {
			t.Fatalf("query: %d, %v", reading, err)
		}
	}

	t.Run("asked", func(t *testing.T) {
		path := pointAtTempDatabase(t)
		rt, err := Open(Options{Migrate: true, OptimizeOnClose: true})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		work(t, rt)
		rt.Close()
		if got := statisticsRows(t, path); got == 0 {
			t.Fatal("the server closed the database without leaving the planner any statistics")
		}
	})

	t.Run("not asked", func(t *testing.T) {
		path := pointAtTempDatabase(t)
		rt, err := Open(Options{Migrate: true})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		work(t, rt)
		rt.Close()
		if got := statisticsRows(t, path); got != 0 {
			t.Fatalf("a run that did not ask for statistics wrote %d rows of them on close", got)
		}
	})
}

func TestRuntimeCloseIsNilSafe(t *testing.T) {
	var rt *Runtime
	rt.Close()
}
