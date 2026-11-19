package connectors

import (
	"slices"
	"sync"
	"time"
)

// SeriesRecordTTL is how long a connector reuses a series record — the part of
// a resolve that describes the series (title, cover, alternate titles,
// metadata) rather than its chapters. Those change on the scale of weeks, while
// the poller resolves a reading tracker every half hour; refetching the record
// each time doubled the requests to MangaFire and MangaDex for nothing. The
// chapter listing is never cached, so freshness is untouched.
const SeriesRecordTTL = 24 * time.Hour

// maxRecordCacheEntries is the most entries a cache holds. A library is a few
// hundred series per site, so the bound is never reached in practice; it is
// there for the caller that is not a library — a link scan resolving and
// searching thousands of candidates in an afternoon.
//
// Reaching it used to sweep only the expired entries, and a scan's entries are
// all fresh: nothing was dropped, the cache kept growing, and every Put past
// the bound walked the whole map to find that out again.
const maxRecordCacheEntries = 4096

// RecordCache keeps values by key for a fixed time, and no more than a fixed
// number of them. It is safe for concurrent use: the poller's shards and the
// dashboard's lookups share one connector.
type RecordCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	limit   int
	entries map[string]recordCacheEntry[T]
	now     func() time.Time
}

type recordCacheEntry[T any] struct {
	value    T
	storedAt time.Time
}

// NewRecordCache returns a cache whose entries live for ttl.
func NewRecordCache[T any](ttl time.Duration) *RecordCache[T] {
	return &RecordCache[T]{ttl: ttl, limit: maxRecordCacheEntries, entries: map[string]recordCacheEntry[T]{}, now: time.Now}
}

// Get returns the value stored under key while it is fresh.
func (c *RecordCache[T]) Get(key string) (T, bool) {
	var zero T
	if c == nil {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || c.now().Sub(entry.storedAt) >= c.ttl {
		return zero, false
	}
	return entry.value, true
}

// Put stores value under key, replacing any older entry.
func (c *RecordCache[T]) Put(key string, value T) {
	c.PutUnless(key, value, nil)
}

// PutUnless stores value under key unless the cache holds a fresh entry there
// that keep approves of, and reports whether it stored. The decision and the
// write happen under one lock, so a better record that lands between a
// caller's Get and its Put is not written over. A nil keep always stores.
func (c *RecordCache[T]) PutUnless(key string, value T, keep func(existing T) bool) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	existing, held := c.entries[key]
	if held && keep != nil && now.Sub(existing.storedAt) < c.ttl && keep(existing.value) {
		return false
	}
	if !held && len(c.entries) >= c.limit {
		c.makeRoom(now)
	}
	c.entries[key] = recordCacheEntry[T]{value: value, storedAt: now}
	return true
}

// makeRoom runs when the cache is full and a new key is about to go in. The
// expired entries go first; when that frees nothing, the oldest eighth goes.
// An eighth rather than one, so the walk and the sort it takes to find the
// oldest are paid once per several hundred stores rather than on each. What is
// evicted costs one refetch if it is asked for again — the entries are a
// cache of the site's own answers, never the only copy of anything.
func (c *RecordCache[T]) makeRoom(now time.Time) {
	for key, entry := range c.entries {
		if now.Sub(entry.storedAt) >= c.ttl {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < c.limit {
		return
	}

	type aged struct {
		key      string
		storedAt time.Time
	}
	byAge := make([]aged, 0, len(c.entries))
	for key, entry := range c.entries {
		byAge = append(byAge, aged{key: key, storedAt: entry.storedAt})
	}
	slices.SortFunc(byAge, func(a, b aged) int { return a.storedAt.Compare(b.storedAt) })
	evict := max(len(byAge)/8, len(byAge)-c.limit+1)
	for _, entry := range byAge[:evict] {
		delete(c.entries, entry.key)
	}
}

// Forget drops key, so the next Get misses: a record a later request proved
// stale (the series moved, or its chapters answer 404) is refetched at once.
func (c *RecordCache[T]) Forget(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}
