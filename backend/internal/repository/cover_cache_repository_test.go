package repository_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func openCoverCacheTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.ApplyMigrations(db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

func TestCoverCacheRoundTripAndExpirySweep(t *testing.T) {
	repo := repository.NewCoverCacheRepository(openCoverCacheTestDB(t))

	fresh := repository.CoverCacheRow{
		CacheKey:  "comick|url:https://comick.dev/comic/kagura-bachi",
		CoverURL:  "https://meo.comick.pictures/KrNwor.jpg",
		SourceKey: "comick",
		Found:     true,
		ExpiresAt: time.Now().UTC().Add(12 * time.Hour),
	}
	expired := repository.CoverCacheRow{
		CacheKey:  "mangafire|item:old",
		CoverURL:  "https://example/old.jpg",
		SourceKey: "mangafire",
		Found:     true,
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}
	negative := repository.CoverCacheRow{
		CacheKey:  "weebcentral|url:https://weebcentral.com/series/x",
		Found:     false,
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}
	for _, entry := range []repository.CoverCacheRow{fresh, expired, negative} {
		if err := repo.Upsert(entry); err != nil {
			t.Fatalf("upsert %q: %v", entry.CacheKey, err)
		}
	}

	entries, err := repo.LoadFresh()
	if err != nil {
		t.Fatalf("load fresh: %v", err)
	}
	byKey := map[string]repository.CoverCacheRow{}
	for _, entry := range entries {
		byKey[entry.CacheKey] = entry
	}
	if len(entries) != 2 {
		t.Fatalf("expected the expired row swept, got %d entries: %v", len(entries), byKey)
	}
	got, ok := byKey[fresh.CacheKey]
	if !ok || got.CoverURL != fresh.CoverURL || !got.Found || got.SourceKey != "comick" {
		t.Fatalf("fresh entry did not round-trip: %+v", got)
	}
	if neg, ok := byKey[negative.CacheKey]; !ok || neg.Found {
		t.Fatalf("negative entry did not round-trip: %+v", neg)
	}
}

// TestCoverCacheLocalEntriesSurviveTheSweep pins what makes local covers
// permanent: an entry whose image lives on disk is exempt from the expiry
// sweep and loads back with its local path, however stale its nominal expiry.
func TestCoverCacheLocalEntriesSurviveTheSweep(t *testing.T) {
	repo := repository.NewCoverCacheRepository(openCoverCacheTestDB(t))

	local := repository.CoverCacheRow{
		CacheKey:  "mangafire|item:local",
		CoverURL:  "https://cdn.example/cover.webp",
		SourceKey: "mangafire",
		Found:     true,
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
		LocalPath: "abc123.webp",
	}
	expiredRemote := repository.CoverCacheRow{
		CacheKey:  "mangafire|item:remote",
		CoverURL:  "https://cdn.example/other.webp",
		SourceKey: "mangafire",
		Found:     true,
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}
	for _, entry := range []repository.CoverCacheRow{local, expiredRemote} {
		if err := repo.Upsert(entry); err != nil {
			t.Fatalf("upsert %q: %v", entry.CacheKey, err)
		}
	}

	entries, err := repo.LoadFresh()
	if err != nil {
		t.Fatalf("load fresh: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the local entry to survive, got %+v", entries)
	}
	if entries[0].CacheKey != local.CacheKey || entries[0].LocalPath != local.LocalPath {
		t.Fatalf("local entry did not round-trip: %+v", entries[0])
	}
}

func TestCoverCacheUpsertReplacesAndDeleteNegatives(t *testing.T) {
	repo := repository.NewCoverCacheRepository(openCoverCacheTestDB(t))

	key := "comick|url:https://comick.dev/comic/x"
	if err := repo.Upsert(repository.CoverCacheRow{CacheKey: key, Found: false, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := repo.Upsert(repository.CoverCacheRow{CacheKey: key, CoverURL: "https://cover", SourceKey: "comick", Found: true, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if err := repo.Upsert(repository.CoverCacheRow{CacheKey: "neg", Found: false, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("negative upsert: %v", err)
	}

	if err := repo.DeleteNegatives(); err != nil {
		t.Fatalf("delete negatives: %v", err)
	}
	entries, err := repo.LoadFresh()
	if err != nil {
		t.Fatalf("load fresh: %v", err)
	}
	if len(entries) != 1 || entries[0].CacheKey != key || !entries[0].Found || entries[0].CoverURL != "https://cover" {
		t.Fatalf("expected only the upgraded positive entry, got %+v", entries)
	}

	if err := repo.Delete(key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entries, err = repo.LoadFresh()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty store, got %+v", entries)
	}
}
