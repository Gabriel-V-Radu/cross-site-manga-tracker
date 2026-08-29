// Package repository is the SQLite persistence layer, one type per table
// family, each holding the shared *sql.DB.
//
// Every method exists twice: an XxxContext variant that carries the
// implementation, and the original name kept as a thin wrapper passing
// context.Background(). The split is not ceremony. The pool runs
// SetMaxOpenConns(1) (internal/database), so any statement — not only a
// transaction — can end up queued behind whoever holds the single connection,
// and a transaction that is never committed or rolled back holds it forever.
// The context-free database/sql calls wait on that queue with no deadline: no
// error, no log line, a process that has silently stopped working. On a
// headless Pi that is the worst failure mode available. Contexts do not
// prevent the bug; they turn an indefinite hang into an error somebody can
// see.
//
// New call sites should take the Context variants and pass the context they
// already have. The wrappers exist so callers that have no context to give yet
// keep compiling.
package repository
