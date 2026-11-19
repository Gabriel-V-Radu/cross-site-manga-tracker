package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openPoolsTestDB(t *testing.T) (writer *sql.DB, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "test.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// TestOpenPairsTheWriterWithAReadOnlyPool pins the two-pool arrangement the
// repositories are built on: one connection that writes, and beside it a pool
// that only reads, sees what the writer committed, and goes when the writer
// is closed.
func TestOpenPairsTheWriterWithAReadOnlyPool(t *testing.T) {
	db, _ := openPoolsTestDB(t)
	reader := Reader(db)
	if reader == db {
		t.Fatal("a database opened by Open must have a read pool of its own")
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("the writer has %d connections, want the one", got)
	}
	if got := reader.Stats().MaxOpenConnections; got != readerConnections {
		t.Fatalf("the read pool allows %d connections, want %d", got, readerConnections)
	}

	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create through the writer: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO notes (body) VALUES ('committed')`); err != nil {
		t.Fatalf("insert through the writer: %v", err)
	}

	// A read issued after a write has returned sees that write.
	var body string
	if err := reader.QueryRow(`SELECT body FROM notes WHERE id = 1`).Scan(&body); err != nil || body != "committed" {
		t.Fatalf("the read pool read %q, %v; want what the writer committed", body, err)
	}

	// A statement that would write through the read pool fails, whatever it
	// is: a misrouted write must not go unnoticed.
	for _, statement := range []string{
		`INSERT INTO notes (body) VALUES ('through the reader')`,
		`UPDATE notes SET body = 'changed'`,
		`DELETE FROM notes`,
		`CREATE TABLE other (id INTEGER)`,
	} {
		if _, err := reader.Exec(statement); err == nil {
			t.Fatalf("the read pool ran %q", statement)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM notes WHERE body = 'committed'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("after the refused writes the table holds %d untouched rows, %v", rows, err)
	}

	// What an open transaction on the writer has done is not visible to a
	// reader until it commits, and the reader does not wait for it either.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`UPDATE notes SET body = 'pending' WHERE id = 1`); err != nil {
		t.Fatalf("write inside the transaction: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reader.QueryRowContext(ctx, `SELECT body FROM notes WHERE id = 1`).Scan(&body); err != nil || body != "committed" {
		t.Fatalf("with a transaction open the read pool read %q, %v; want the committed row, at once", body, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := reader.QueryRow(`SELECT body FROM notes WHERE id = 1`).Scan(&body); err != nil || body != "pending" {
		t.Fatalf("after the commit the read pool read %q, %v", body, err)
	}

	// Closing the writer is closing the database: the readers go with it, or
	// a caller that only ever knew about the *sql.DB it was given would leave
	// the file open behind it.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := reader.Ping(); err == nil {
		t.Fatal("the read pool outlived the writer it was opened beside")
	}
	if Reader(db) != db {
		t.Fatal("a closed database must not go on being paired with its read pool")
	}
}

// A handle that did not come from Open has no read pool; Reader answers with
// the handle itself, so a repository built on it reads where it writes.
func TestReaderOfAForeignHandleIsTheHandle(t *testing.T) {
	foreign, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "foreign.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	if Reader(foreign) != foreign {
		t.Fatal("Reader invented a read pool for a handle Open did not return")
	}
}

// TestOpenAppliesThePoolsOwnPragmas: both pools get the page cache and the
// in-memory temp store, the writer keeps the pragmas it always had, and only
// the readers are query_only.
func TestOpenAppliesThePoolsOwnPragmas(t *testing.T) {
	db, _ := openPoolsTestDB(t)
	assertOpenPragmas(t, db, "writer")

	pragma := func(handle *sql.DB, name string) int {
		t.Helper()
		var value int
		if err := handle.QueryRow(`PRAGMA ` + name).Scan(&value); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return value
	}
	for name, handle := range map[string]*sql.DB{"writer": db, "reader": Reader(db)} {
		// temp_store: 2 is MEMORY.
		if cache, temp, busy := pragma(handle, "cache_size"), pragma(handle, "temp_store"), pragma(handle, "busy_timeout"); cache != -16000 || temp != 2 || busy != 5000 {
			t.Fatalf("%s: cache_size=%d temp_store=%d busy_timeout=%d, want -16000, 2, 5000", name, cache, temp, busy)
		}
	}
	if got := pragma(db, "query_only"); got != 0 {
		t.Fatalf("the writer is query_only=%d", got)
	}
	if got := pragma(Reader(db), "query_only"); got != 1 {
		t.Fatalf("the read pool is query_only=%d, want 1", got)
	}
	var journalMode string
	if err := Reader(db).QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil || !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("the read pool sees journal_mode=%q, %v; want the writer's wal", journalMode, err)
	}
}

// TestWriterTransactionsTakeTheWriteLockAtBegin pins _txlock=immediate. A
// transaction that reads and then writes, begun the default way, holds a read
// lock until its first write and has to upgrade it then; SQLite does not wait
// out busy_timeout for an upgrade, so with anyone else holding the write lock
// at that moment — a backup, a CLI tool on the live file — the write failed at
// once with SQLITE_BUSY. Begun with the write lock, the transaction waits for
// it where waiting works.
func TestWriterTransactionsTakeTheWriteLockAtBegin(t *testing.T) {
	db, path := openPoolsTestDB(t)
	if _, err := db.Exec(`CREATE TABLE counters (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO counters (id, value) VALUES (1, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Someone else on the same file: its own connection, no patience.
	outsider, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open the outside connection: %v", err)
	}
	t.Cleanup(func() { _ = outsider.Close() })
	outsider.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := outsider.Conn(ctx)
	if err != nil {
		t.Fatalf("take the outside connection: %v", err)
	}
	defer conn.Close()

	// The outsider holds the write lock for a while, then lets go.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("outside begin: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE counters SET value = value + 10 WHERE id = 1`); err != nil {
		t.Fatalf("outside write: %v", err)
	}
	const held = 300 * time.Millisecond
	released := make(chan error, 1)
	go func() {
		time.Sleep(held)
		_, err := conn.ExecContext(ctx, `COMMIT`)
		released <- err
	}()

	// Ours reads, then writes, as nearly every repository transaction does.
	started := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin while the lock is held elsewhere: %v", err)
	}
	defer tx.Rollback()
	var value int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM counters WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("read inside the transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE counters SET value = ? WHERE id = 1`, value+1); err != nil {
		t.Fatalf("the write after the read failed instead of having waited for the lock: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if waited := time.Since(started); waited < held/2 {
		t.Fatalf("the transaction took %s: it did not wait for the lock the outsider held for %s", waited, held)
	}
	if err := <-released; err != nil {
		t.Fatalf("outside commit: %v", err)
	}

	// Having waited, it read what the outsider committed and built on it.
	if err := db.QueryRow(`SELECT value FROM counters WHERE id = 1`).Scan(&value); err != nil || value != 11 {
		t.Fatalf("value = %d, %v; want the outsider's 10 and our 1", value, err)
	}
}

// Optimize is the statistics pass the server runs before closing. It must run
// on the writer without complaint on a database with rows and queries behind
// it, and leave it readable.
func TestOptimizeRunsOnTheWriter(t *testing.T) {
	db, _ := openPoolsTestDB(t)
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var sources int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sources WHERE enabled = 1`).Scan(&sources); err != nil {
		t.Fatalf("query: %v", err)
	}
	if err := Optimize(context.Background(), db); err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if err := Reader(db).QueryRow(`SELECT COUNT(1) FROM sources WHERE enabled = 1`).Scan(&sources); err != nil || sources == 0 {
		t.Fatalf("after optimize the read pool read %d sources, %v", sources, err)
	}
}
