package app

import (
	"path/filepath"
	"testing"
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

func TestRuntimeCloseIsNilSafe(t *testing.T) {
	var rt *Runtime
	rt.Close()
}
