// Package repository is the SQLite persistence layer, one type per table
// family, each holding the database's two handles (database.Open): the
// writer, which every write and every transaction goes through, and the
// read-only pool its plain reads go to.
//
// Every method takes a context.Context as its first parameter, and callers
// pass the one they already have (the request's, the poll cycle's). The
// writer runs SetMaxOpenConns(1) (internal/database), so any write — not only
// a transaction — can end up queued behind whoever holds its single
// connection, and a transaction that is never committed or rolled back holds
// it forever. A context-free database/sql call waits on that queue with no
// deadline: no error, no log line, a process that has silently stopped
// working. On a headless Pi that is the worst failure mode available.
// Contexts do not prevent the bug; they turn an indefinite hang into an error
// somebody can see. That is why there is no context-free variant to reach for.
//
// Reads are not in that queue: a listing is answered from what is committed
// while a write is in progress. Inside a transaction, though, every statement
// — its reads included — goes through the transaction's own handle: a read
// sent to the pool from there would not see what the transaction has written.
package repository
