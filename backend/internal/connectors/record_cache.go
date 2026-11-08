package connectors

import (
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

// maxRecordCacheEntries bounds a cache: expired entries are swept when it grows
// past this. A library is a few hundred series per site, so the bound is never
// reached in practice; it only stops an unusual caller (a scan resolving
// thousands of candidates) from growing it without limit.
const maxRecordCacheEntries = 4096

// RecordCache keeps values by key for a fixed time. It is safe for concurrent
// use: the poller's shards and the dashboard's lookups share one connector.
type RecordCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]recordCacheEntry[T]
	now     func() time.Time
}

type recordCacheEntry[T any] struct {
	value    T
	storedAt time.Time
}

// NewRecordCache returns a cache whose entries live for ttl.
func NewRecordCache[T any](ttl time.Duration) *RecordCache[T] {
	return &RecordCache[T]{ttl: ttl, entries: map[string]recordCacheEntry[T]{}, now: time.Now}
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
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= maxRecordCacheEntries {
		for existing, entry := range c.entries {
			if now.Sub(entry.storedAt) >= c.ttl {
				delete(c.entries, existing)
			}
		}
	}
	c.entries[key] = recordCacheEntry[T]{value: value, storedAt: now}
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
