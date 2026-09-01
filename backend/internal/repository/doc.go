// Package repository is the SQLite persistence layer, one type per table
// family, each holding the shared *sql.DB.
//
// Every method takes a context.Context as its first parameter, and callers
// pass the one they already have (the request's, the poll cycle's). The pool
// runs SetMaxOpenConns(1) (internal/database), so any statement — not only a
// transaction — can end up queued behind whoever holds the single connection,
// and a transaction that is never committed or rolled back holds it forever.
// A context-free database/sql call waits on that queue with no deadline: no
// error, no log line, a process that has silently stopped working. On a
// headless Pi that is the worst failure mode available. Contexts do not
// prevent the bug; they turn an indefinite hang into an error somebody can
// see. That is why there is no context-free variant to reach for.
package repository
