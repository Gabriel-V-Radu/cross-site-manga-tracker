package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// TWO POOLS. Open returns the writer: one connection, the only one that may
// change the database. Beside it, it opens a pool of read-only connections
// that Reader hands out and the repositories send their plain reads to.
//
// With the single connection alone, everything queued behind whoever held it:
// a dashboard page behind the poller's write, behind the metadata pass, behind
// a link scan. WAL lets readers run while a writer writes, and one connection
// never used that. The writer stays single because SQLite has one writer at a
// time anyway and a single connection makes that explicit — two writing
// connections of our own could contend hard enough to surface SQLITE_BUSY.
//
// The reader connections run with query_only: a statement that would write
// through one fails, loudly and in every test that touches it, rather than
// contending with the writer unnoticed. What a reader sees is everything
// committed when its statement starts, so a read issued after a write has
// returned sees that write — but a read issued while a transaction is still
// open on the writer does not see what that transaction has done so far, and
// inside a transaction every statement has to go through the transaction's
// handle, as it always had to.
//
// The pairing lives in this package (readers) rather than in a type of its
// own so that Open still returns a *sql.DB: every caller that only ever had a
// *sql.DB — the tools, the handlers, each test's helpers — goes on working as
// it was, on the writer, and closing it closes the readers too.
func Open(sqlitePath string) (*sql.DB, error) {
	dir := filepath.Dir(sqlitePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite dir: %w", err)
	}

	// sql.Open connects lazily; nothing touches the file until the pings.
	reader, err := sql.Open("sqlite", dataSourceName(sqlitePath, readerPragmas))
	if err != nil {
		return nil, fmt.Errorf("open sqlite readers: %w", err)
	}
	reader.SetMaxOpenConns(readerConnections)
	reader.SetMaxIdleConns(readerConnections)

	connector := &writerConnector{
		driver: reader.Driver(),
		dsn:    dataSourceName(sqlitePath, writerPragmas),
		reader: reader,
	}
	db := sql.OpenDB(connector)
	connector.writer = db
	// One connection: see above. It is not what keeps the PRAGMAs in force:
	// those come from the DSN, for every connection a pool opens.
	db.SetMaxOpenConns(1)

	// The writer first: it creates the file and puts it in WAL mode, which the
	// readers then find in place. A database that cannot take the DSN's
	// PRAGMAs fails here.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite connection and apply pragmas: %w", err)
	}
	if err := reader.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read connection and apply pragmas: %w", err)
	}

	readers.Store(db, reader)
	return db, nil
}

// readerConnections is the size of the read pool. Four is the Pi's core
// count, and more than the dashboard ever has in flight at once: a page of
// cards is a handful of listings, issued one after another.
const readerConnections = 4

// readers pairs every writer Open returned with its read pool, until the
// writer is closed.
var readers sync.Map // *sql.DB -> *sql.DB

// Reader returns the read-only pool opened beside db by Open. For a *sql.DB
// that did not come from Open it returns db itself, so code written against
// it works on any handle.
func Reader(db *sql.DB) *sql.DB {
	if reader, ok := readers.Load(db); ok {
		return reader.(*sql.DB)
	}
	return db
}

// writerConnector opens the writer's connections and, because database/sql
// closes a connector that is an io.Closer when the DB it belongs to is closed,
// is how closing the writer closes its readers.
type writerConnector struct {
	driver driver.Driver
	dsn    string
	writer *sql.DB
	reader *sql.DB
}

func (c *writerConnector) Connect(context.Context) (driver.Conn, error) {
	return c.driver.Open(c.dsn)
}

func (c *writerConnector) Driver() driver.Driver { return c.driver }

func (c *writerConnector) Close() error {
	readers.Delete(c.writer)
	return c.reader.Close()
}

// Optimize runs PRAGMA optimize on the writer, as SQLite asks of a
// long-running application before it closes its connection: the tables whose
// indexes the connection's own queries would have been planned better with
// statistics get them (sqlite_stat1), and every later plan is chosen on the
// table sizes the library really has rather than on the planner's defaults.
// It is quick — nothing is analyzed that does not need it — and its failure
// costs nothing but the statistics.
//
// It is the caller's to ask for, not part of closing: it writes, and a
// preview run of a tool must leave the database as it found it.
func Optimize(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("optimize sqlite: %w", err)
	}
	return nil
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
//   - cache_size(-16000): sixteen megabytes of page cache per connection in
//     place of SQLite's two. The whole library fits in it several times over,
//     so a listing is answered from memory instead of from the SD card.
//   - temp_store(MEMORY): the sorts and the UNION the listings build go to
//     memory, not to a temporary file on that same card.
//
// Nothing else belongs in the DSN. In particular no _time_format: the stored
// timestamp spelling described above Open depends on its absence.
const connectionPragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=cache_size(-16000)" +
	"&_pragma=temp_store(MEMORY)"

// writerPragmas adds the one setting that is the writer's alone.
//
// _txlock=immediate makes every transaction open with BEGIN IMMEDIATE. Nearly
// all of ours read and then write — check the tracker is the profile's, then
// update it — and under the default, BEGIN DEFERRED, such a transaction holds
// only a read lock until its first write and has to upgrade it then. SQLite
// does not wait out busy_timeout for an upgrade: if someone else holds the
// write lock at that moment (a backup, a CLI tool against the live file) it
// fails at once with SQLITE_BUSY, the timeout notwithstanding. Taking the
// write lock at BEGIN is the one point where the timeout applies.
const writerPragmas = connectionPragmas + "&_txlock=immediate"

// readerPragmas are the read pool's: the same, and query_only, which refuses
// any statement that would change the database (see the note above Open).
// journal_mode is left out — it is a property of the file, set by the writer
// before a reader is opened, and not a reader's to set.
const readerPragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=cache_size(-16000)" +
	"&_pragma=temp_store(MEMORY)" +
	"&_pragma=query_only(1)"

// dataSourceName appends the pragmas to the configured path. The driver cuts
// a DSN at its first "?" and opens what precedes it as a plain file name
// unless it starts with "file:", so a Windows path (C:\dir\app.sqlite) and a
// relative one (./data/app.sqlite) reach SQLite exactly as they were given. A
// path that already carries parameters keeps them.
func dataSourceName(sqlitePath string, pragmas string) string {
	separator := "?"
	if strings.Contains(sqlitePath, "?") {
		separator = "&"
	}
	return sqlitePath + separator + pragmas
}
