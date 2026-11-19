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

// TestRecordCacheEvictsTheOldestWhenFullOfFreshEntries pins the bound itself.
// Reaching it only ever swept expired entries, and a link scan's are all
// fresh: nothing went, the map grew past the bound without limit, and every
// store from there on walked all of it to drop nothing. The oldest go, in a
// batch, and the cache never holds more than its limit.
func TestRecordCacheEvictsTheOldestWhenFullOfFreshEntries(t *testing.T) {
	clock := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cache := NewRecordCache[int](time.Hour)
	cache.now = func() time.Time { return clock }
	cache.limit = 16

	for index := 0; index < 16; index++ {
		cache.Put(fmt.Sprint(index), index)
		clock = clock.Add(time.Second)
	}
	// Replacing a key that is already held needs no room.
	cache.Put("0", 100)
	if len(cache.entries) != 16 {
		t.Fatalf("replacing a held key changed the size to %d", len(cache.entries))
	}

	cache.Put("new", 1)
	if len(cache.entries) != 15 {
		t.Fatalf("expected an eighth evicted and the new entry stored, %d entries held", len(cache.entries))
	}
	// "0" was stored again a moment ago, so the two oldest are "1" and "2".
	for _, evicted := range []string{"1", "2"} {
		if _, ok := cache.Get(evicted); ok {
			t.Fatalf("expected the oldest entry %q evicted", evicted)
		}
	}
	for _, kept := range []string{"0", "3", "15", "new"} {
		if _, ok := cache.Get(kept); !ok {
			t.Fatalf("expected %q kept", kept)
		}
	}

	for index := 0; index < 500; index++ {
		clock = clock.Add(time.Second)
		cache.Put(fmt.Sprintf("scan-%d", index), index)
		if len(cache.entries) > cache.limit {
			t.Fatalf("the cache holds %d entries, over its limit of %d", len(cache.entries), cache.limit)
		}
	}
	if _, ok := cache.Get("scan-499"); !ok {
		t.Fatal("the entry stored last was not kept")
	}
}

// TestRecordCachePutUnlessKeepsWhatTheCallerApproves pins the conditional
// store: the entry held is kept only while it is fresh and keep says so.
func TestRecordCachePutUnlessKeepsWhatTheCallerApproves(t *testing.T) {
	clock := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cache := NewRecordCache[string](time.Hour)
	cache.now = func() time.Time { return clock }
	isFull := func(existing string) bool { return existing == "full" }

	if !cache.PutUnless("a", "slim", isFull) {
		t.Fatal("nothing was held, so the value had to be stored")
	}
	if !cache.PutUnless("a", "full", isFull) {
		t.Fatal("the held value was not one to keep, so it had to be replaced")
	}
	if cache.PutUnless("a", "slim", isFull) {
		t.Fatal("a held value the caller approves was replaced")
	}
	if value, _ := cache.Get("a"); value != "full" {
		t.Fatalf("held %q, want the approved value", value)
	}

	// Expired, it is no longer worth keeping whatever it holds.
	clock = clock.Add(time.Hour)
	if !cache.PutUnless("a", "slim", isFull) {
		t.Fatal("an expired value was kept")
	}
	if value, ok := cache.Get("a"); !ok || value != "slim" {
		t.Fatalf("held %q (fresh=%v), want the new value", value, ok)
	}
}

func TestRecordCacheNilIsSafe(t *testing.T) {
	var cache *RecordCache[string]
	cache.Put("a", "b")
	if cache.PutUnless("a", "b", nil) {
		t.Fatal("nil cache stored")
	}
	cache.Forget("a")
	if _, ok := cache.Get("a"); ok {
		t.Fatal("nil cache hit")
	}
}
