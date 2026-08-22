package handlers

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func openCoverPersistenceTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.ApplyMigrations(db, filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	return db
}

// TestCoverCacheSurvivesHandlerRestart pins why the cover cache is persisted
// at all: an in-memory-only cache made every container restart re-resolve
// every card's cover against rate-limited sources.
func TestCoverCacheSurvivesHandlerRestart(t *testing.T) {
	db := openCoverPersistenceTestDB(t)

	first := NewDashboardHandler(db, nil)
	cacheKey := buildCoverCacheKey("comick", "https://comick.dev/comic/kagura-bachi", nil)
	first.setCachedCoverFromSource(cacheKey, "https://meo.comick.pictures/KrNwor.jpg", "comick", true, 12*time.Hour)

	// A second handler over the same DB models the restarted process.
	second := NewDashboardHandler(db, nil)
	coverURL, sourceKey, found, ok := second.getCachedCoverWithSource(cacheKey)
	if !ok || !found {
		t.Fatalf("expected the cover to survive the restart, got ok=%v found=%v", ok, found)
	}
	if coverURL != "https://meo.comick.pictures/KrNwor.jpg" || sourceKey != "comick" {
		t.Fatalf("unexpected persisted cover: %q from %q", coverURL, sourceKey)
	}
}

// TestNegativeCoverInvalidationClearsStore mirrors the in-memory behavior:
// once a tracker's links change, remembered "no cover found" entries must not
// survive anywhere, or a fresh link would look broken for a whole TTL.
func TestNegativeCoverInvalidationClearsStore(t *testing.T) {
	db := openCoverPersistenceTestDB(t)

	first := NewDashboardHandler(db, nil)
	negativeKey := buildCoverCacheKey("mangafire", "https://mangafire.to/title/x", nil)
	first.setCachedCover(negativeKey, "", false, time.Hour)
	positiveKey := buildCoverCacheKey("comick", "https://comick.dev/comic/y", nil)
	first.setCachedCoverFromSource(positiveKey, "https://cover", "comick", true, time.Hour)

	first.invalidateLinkLookups()

	second := NewDashboardHandler(db, nil)
	if _, _, _, ok := second.getCachedCoverWithSource(negativeKey); ok {
		t.Fatalf("negative entry must not survive invalidation")
	}
	if _, _, found, ok := second.getCachedCoverWithSource(positiveKey); !ok || !found {
		t.Fatalf("positive entry must survive invalidation")
	}
}

// TestExpiredPersistedCoversAreNotServed guards the seed path: rows past their
// TTL must not warm the cache.
func TestExpiredPersistedCoversAreNotServed(t *testing.T) {
	db := openCoverPersistenceTestDB(t)

	store := repository.NewCoverCacheRepository(db)
	expiredKey := buildCoverCacheKey("comick", "https://comick.dev/comic/z", nil)
	if err := store.Upsert(repository.CoverCacheRow{
		CacheKey:  expiredKey,
		CoverURL:  "https://stale",
		SourceKey: "comick",
		Found:     true,
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed expired row: %v", err)
	}

	handler := NewDashboardHandler(db, nil)
	if _, _, _, ok := handler.getCachedCoverWithSource(expiredKey); ok {
		t.Fatalf("expired persisted cover must not be served")
	}
}
