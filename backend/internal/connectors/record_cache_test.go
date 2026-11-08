package connectors

import (
	"fmt"
	"testing"
	"time"
)

func TestRecordCacheExpiresAndForgets(t *testing.T) {
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cache := NewRecordCache[string](time.Hour)
	cache.now = func() time.Time { return clock }

	if _, ok := cache.Get("a"); ok {
		t.Fatal("empty cache hit")
	}
	cache.Put("a", "one")
	if value, ok := cache.Get("a"); !ok || value != "one" {
		t.Fatalf("Get = %q, %v", value, ok)
	}
	clock = clock.Add(59 * time.Minute)
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("entry expired early")
	}
	clock = clock.Add(time.Minute)
	if _, ok := cache.Get("a"); ok {
		t.Fatal("entry outlived its ttl")
	}
	cache.Put("b", "two")
	cache.Forget("b")
	if _, ok := cache.Get("b"); ok {
		t.Fatal("forgotten entry still served")
	}
}

func TestRecordCacheSweepsExpiredEntriesWhenFull(t *testing.T) {
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cache := NewRecordCache[int](time.Hour)
	cache.now = func() time.Time { return clock }
	for index := 0; index < maxRecordCacheEntries; index++ {
		cache.Put(fmt.Sprint(index), index)
	}
	clock = clock.Add(2 * time.Hour)
	cache.Put("fresh", 1)
	if len(cache.entries) != 1 {
		t.Fatalf("expected the expired entries swept, %d left", len(cache.entries))
	}
}

func TestRecordCacheNilIsSafe(t *testing.T) {
	var cache *RecordCache[string]
	cache.Put("a", "b")
	cache.Forget("a")
	if _, ok := cache.Get("a"); ok {
		t.Fatal("nil cache hit")
	}
}
