package database

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

// unicode_lower is lower() with Go's Unicode case mapping. SQLite's built-in
// lower() folds ASCII only, so a filter that lowers its input in Go and
// compares it with LOWER(column) silently misses every value with a non-ASCII
// capital: the title search lowered 'Ōkami' to 'ōkami' while SQL left the
// column as 'Ōkami'. The driver attaches registered functions to every
// connection opened afterwards and its registry takes no lock, so the one
// registration happens here, before anything can call Open.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("unicode_lower", 1, unicodeLower)
}

func unicodeLower(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	switch value := args[0].(type) {
	case string:
		return strings.ToLower(value), nil
	case []byte:
		return strings.ToLower(string(value)), nil
	default:
		// NULL stays NULL, and a number has no case to fold.
		return value, nil
	}
}

// TIMESTAMP COLUMNS HOLD TWO DIFFERENT SPELLINGS. Read this before writing a
// query that does date arithmetic in SQL.
//
// The DSN names no _time_format (it carries only the PRAGMAs listed at
// connectionPragmas), so modernc.org/sqlite binds a time.Time with
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

	db, err := sql.Open("sqlite", dataSourceName(sqlitePath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// One connection, not a pool. Two of our own connections could contend
	// hard enough to surface SQLITE_BUSY (a link scan writing while the
	// dashboard read). SQLite serializes writers anyway; a single connection
	// makes the serialization explicit. It is not what keeps the PRAGMAs in
	// force: those come from the DSN, for every connection the pool opens.
	db.SetMaxOpenConns(1)

	// sql.Open connects lazily, so this is where the file is opened and the
	// DSN's PRAGMAs run; a database that cannot take them fails here.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite connection and apply pragmas: %w", err)
	}

	return db, nil
}

// connectionPragmas are the settings every connection must run under, spelled
// as the driver's `_pragma` DSN parameters: modernc.org/sqlite executes each
// one as a PRAGMA statement on every connection it opens (busy_timeout first,
// the rest alphabetically).
//
// They used to be issued once through db.Exec, which only configures the
// connection that happens to run them. database/sql replaces a connection the
// driver reports as bad — this driver does for one still flagged as
// interrupted, which a cancelled statement can leave behind — without telling
// anyone, and the replacement came up on SQLite's defaults: foreign keys OFF,
// so ON DELETE CASCADE stopped cascading, and no busy timeout.
//
//   - busy_timeout: still worth having with one connection. External processes
//     (backups, the CLI tools) can hold the file, and waiting beats failing.
//   - foreign_keys: off by default in SQLite; the schema's cascades need it.
//   - journal_mode=WAL: readers do not block the writer.
//   - synchronous=NORMAL: the default, FULL, fsyncs on every commit, and the
//     poller commits once per polled tracker — hundreds of fsyncs per cycle on
//     the Pi. In WAL mode NORMAL syncs at checkpoints only and still cannot
//     corrupt the database. A crash of the process loses nothing; a power cut
//     can lose the commits the OS had not flushed yet, a few seconds' worth.
//     Those are nearly always poll results the next cycle writes again, but
//     an edit made in those same seconds would go with them.
//
// Nothing else belongs in the DSN. In particular no _time_format: the stored
// timestamp spelling described above Open depends on its absence.
const connectionPragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)"

// dataSourceName appends the pragmas to the configured path. The driver cuts
// a DSN at its first "?" and opens what precedes it as a plain file name
// unless it starts with "file:", so a Windows path (C:\dir\app.sqlite) and a
// relative one (./data/app.sqlite) reach SQLite exactly as they were given. A
// path that already carries parameters keeps them.
func dataSourceName(sqlitePath string) string {
	separator := "?"
	if strings.Contains(sqlitePath, "?") {
		separator = "&"
	}
	return sqlitePath + separator + connectionPragmas
}
