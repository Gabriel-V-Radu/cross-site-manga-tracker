package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readPragmas reads the settings Open is responsible for from whichever
// connection the pool hands out.
func readPragmas(t *testing.T, db *sql.DB) (synchronous int, foreignKeys int, busyTimeout int, journalMode string) {
	t.Helper()
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	return synchronous, foreignKeys, busyTimeout, journalMode
}

func assertOpenPragmas(t *testing.T, db *sql.DB, when string) {
	t.Helper()
	synchronous, foreignKeys, busyTimeout, journalMode := readPragmas(t, db)
	// synchronous: 1 is NORMAL (2, the default, is FULL).
	if synchronous != 1 || foreignKeys != 1 || busyTimeout != 5000 || !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("%s: synchronous=%d foreign_keys=%d busy_timeout=%d journal_mode=%q, want 1, 1, 5000, wal",
			when, synchronous, foreignKeys, busyTimeout, journalMode)
	}
}

// TestOpenAppliesItsPragmasToAReplacedConnection pins why the pragmas travel
// in the DSN. database/sql drops a connection the driver reports as bad and
// opens another behind the caller's back; settings issued once through Exec
// stayed on the dropped one, and the replacement ran with foreign keys off —
// ON DELETE CASCADE quietly not cascading — and no busy timeout.
func TestOpenAppliesItsPragmasToAReplacedConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	assertOpenPragmas(t, db, "first connection")

	// A temp table lives and dies with its connection, so it tells the
	// replacement from the original.
	if _, err := db.Exec(`CREATE TEMP TABLE connection_marker (id INTEGER)`); err != nil {
		t.Fatalf("mark the connection: %v", err)
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("take the connection: %v", err)
	}
	// Reporting ErrBadConn from Raw is how a driver-level failure looks to
	// database/sql: the pool closes the connection instead of taking it back.
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); err != driver.ErrBadConn {
		t.Fatalf("Raw = %v, want ErrBadConn", err)
	}
	_ = conn.Close()

	var marker int
	if err := db.QueryRow(`SELECT COUNT(1) FROM connection_marker`).Scan(&marker); err == nil {
		t.Fatal("the connection was not replaced: its temp table is still there")
	}

	assertOpenPragmas(t, db, "replaced connection")
}

// TestOpenKeepsTheStoredTimestampSpelling guards the contract db.go's long
// comment describes: a time.Time is stored as time.Time.String() and both
// that spelling and CURRENT_TIMESTAMP's read back as times. The DSN decides
// the stored spelling, so anything added to it has to leave this alone —
// rows written before and after must keep comparing as text.
func TestOpenKeepsTheStoredTimestampSpelling(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE stamps (id INTEGER PRIMARY KEY, at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	stamp := time.Date(2026, 8, 29, 14, 3, 22, 123456789, time.UTC)
	if _, err := db.Exec(`INSERT INTO stamps (id, at) VALUES (1, ?)`, stamp); err != nil {
		t.Fatalf("insert a Go time: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO stamps (id) VALUES (2)`); err != nil {
		t.Fatalf("insert a defaulted time: %v", err)
	}

	// Concatenating drops the column's DATETIME type, so the driver hands the
	// text back as stored instead of parsing it.
	var raw string
	if err := db.QueryRow(`SELECT at || '' FROM stamps WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("read the stored text: %v", err)
	}
	const want = "2026-08-29 14:03:22.123456789 +0000 UTC"
	if raw != want || raw != stamp.String() {
		t.Fatalf("a Go time is stored as %q, want %q (time.Time.String())", raw, want)
	}

	var readBack time.Time
	if err := db.QueryRow(`SELECT at FROM stamps WHERE id = 1`).Scan(&readBack); err != nil {
		t.Fatalf("read the Go time back: %v", err)
	}
	if !readBack.Equal(stamp) {
		t.Fatalf("read back %s, want %s", readBack, stamp)
	}

	var defaultedRaw string
	if err := db.QueryRow(`SELECT at || '' FROM stamps WHERE id = 2`).Scan(&defaultedRaw); err != nil {
		t.Fatalf("read the defaulted text: %v", err)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", defaultedRaw); err != nil {
		t.Fatalf("CURRENT_TIMESTAMP stored %q, want the nineteen-character spelling", defaultedRaw)
	}
	var defaulted time.Time
	if err := db.QueryRow(`SELECT at FROM stamps WHERE id = 2`).Scan(&defaulted); err != nil {
		t.Fatalf("read the defaulted time back: %v", err)
	}
	if time.Since(defaulted) > time.Hour || time.Until(defaulted) > time.Hour {
		t.Fatalf("the defaulted time read back as %s", defaulted)
	}
}

// TestOpenTreatsARelativePathAsThePathItWasGiven covers the deployed
// spelling, "./data/app.sqlite": the pragmas ride behind a "?" on the DSN and
// the driver has to cut them off again before it opens the file, or the
// database would be created under a name that carries them.
func TestOpenTreatsARelativePathAsThePathItWasGiven(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	db, err := Open("./data/app.sqlite")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Registered as well as called below: a failed assertion must not leave
	// the file open under the temp dir Windows is about to remove.
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE probe (id INTEGER)`); err != nil {
		t.Fatalf("write through the connection: %v", err)
	}
	assertOpenPragmas(t, db, "relative path")
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("read the data dir: %v", err)
	}
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if name == "app.sqlite" {
			found = true
			continue
		}
		// The WAL and its index may outlive the close; nothing else belongs.
		if name != "app.sqlite-wal" && name != "app.sqlite-shm" {
			t.Fatalf("Open left an unexpected file %q beside the database", name)
		}
	}
	if !found {
		t.Fatal("the database was not created at ./data/app.sqlite")
	}
}
