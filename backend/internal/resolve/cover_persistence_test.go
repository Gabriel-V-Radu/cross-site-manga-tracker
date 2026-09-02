package resolve

import (
	"context"
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

	if err := database.ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	return db
}

// newPersistentCoverResolver models the running app's resolver minus the
// network: it writes through to the store, which is what these tests are about.
func newPersistentCoverResolver(t *testing.T, db *sql.DB) *CoverResolver {
	t.Helper()
	resolver := NewCoverResolver(CoverConfig{
		Store: repository.NewCoverCacheRepository(db),
		Dir:   t.TempDir(),
	})
	t.Cleanup(resolver.Close)
	return resolver
}

// TestCoverCacheSurvivesRestart pins why the cover cache is persisted at all:
// an in-memory-only cache made every container restart re-resolve every card's
// cover against rate-limited sources.
func TestCoverCacheSurvivesRestart(t *testing.T) {
	db := openCoverPersistenceTestDB(t)

	first := newPersistentCoverResolver(t, db)
	cacheKey := coverCacheKey("comick", "https://comick.dev/comic/kagura-bachi", nil)
	first.cacheResultFromSource(cacheKey, "https://meo.comick.pictures/KrNwor.jpg", "comick", true, 12*time.Hour)

	// A second resolver over the same DB models the restarted process.
	second := newPersistentCoverResolver(t, db)
	coverURL, sourceKey, found, ok := second.cachedWithSource(cacheKey)
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

	first := newPersistentCoverResolver(t, db)
	negativeKey := coverCacheKey("mangafire", "https://mangafire.to/title/x", nil)
	first.cacheResult(negativeKey, "", false, time.Hour)
	positiveKey := coverCacheKey("comick", "https://comick.dev/comic/y", nil)
	first.cacheResultFromSource(positiveKey, "https://cover", "comick", true, time.Hour)

	first.InvalidateNegatives()

	second := newPersistentCoverResolver(t, db)
	if _, _, _, ok := second.cachedWithSource(negativeKey); ok {
		t.Fatalf("negative entry must not survive invalidation")
	}
	if _, _, found, ok := second.cachedWithSource(positiveKey); !ok || !found {
		t.Fatalf("positive entry must survive invalidation")
	}
}

// TestExpiredPersistedCoversAreNotServed guards the seed path: rows past their
// TTL must not warm the cache.
func TestExpiredPersistedCoversAreNotServed(t *testing.T) {
	db := openCoverPersistenceTestDB(t)

	store := repository.NewCoverCacheRepository(db)
	expiredKey := coverCacheKey("comick", "https://comick.dev/comic/z", nil)
	if err := store.Upsert(context.Background(), repository.CoverCacheRow{
		CacheKey:  expiredKey,
		CoverURL:  "https://stale",
		SourceKey: "comick",
		Found:     true,
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed expired row: %v", err)
	}

	resolver := newPersistentCoverResolver(t, db)
	if _, _, _, ok := resolver.cachedWithSource(expiredKey); ok {
		t.Fatalf("expired persisted cover must not be served")
	}
}
