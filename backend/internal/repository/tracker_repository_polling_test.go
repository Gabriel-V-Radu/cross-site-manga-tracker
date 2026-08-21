package repository_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func openPollingTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.ApplyMigrations(db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	return db
}

func seedPollingTracker(t *testing.T, db *sql.DB, latestKnownChapter float64) int64 {
	t.Helper()

	result, err := db.Exec(`
		INSERT INTO trackers (title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?)
	`, "Polling Tracker", 1, "https://mangadex.org/title/polling", "reading", latestKnownChapter)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seeded tracker id: %v", err)
	}
	return id
}

func chapterSeenAt(t *testing.T, db *sql.DB, id int64) *time.Time {
	t.Helper()

	var seenAt sql.NullTime
	if err := db.QueryRow(`SELECT latest_chapter_seen_at FROM trackers WHERE id = ?`, id).Scan(&seenAt); err != nil {
		t.Fatalf("read latest_chapter_seen_at: %v", err)
	}
	if !seenAt.Valid {
		return nil
	}
	value := seenAt.Time.UTC()
	return &value
}

// TestUpdatePollingStateStampsChapterSeenAtWhenChapterMoves covers the sources that
// report a chapter number without a release date — MangaDex serves lastChapter for a
// series whose English feed is empty, MangaBuddy reports no usable dates. Recording
// when the number changed is what gives those trackers a date that moves only when a
// chapter does, so "Latest chapter" no longer has to fall back to a polling timestamp.
func TestUpdatePollingStateStampsChapterSeenAtWhenChapterMoves(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	if chapterSeenAt(t, db, trackerID) != nil {
		t.Fatalf("expected the seeded tracker to carry no first-seen timestamp")
	}

	newChapter := 20.0
	discoveredAt := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	if err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, LatestKnownChapter: &newChapter, ClearLatestReleaseAt: true, CheckedAt: discoveredAt}); err != nil {
		t.Fatalf("update polling state: %v", err)
	}

	seenAt := chapterSeenAt(t, db, trackerID)
	if seenAt == nil {
		t.Fatalf("expected a first-seen timestamp for the new chapter")
	}
	if !seenAt.Equal(discoveredAt) {
		t.Fatalf("expected first-seen %s, got %s", discoveredAt, *seenAt)
	}

	// A later poll that finds the same chapter must not restate when it appeared:
	// re-stamping on every cycle is exactly the drift that made a dateless tracker
	// look freshly updated forever.
	laterCheck := discoveredAt.Add(6 * time.Hour)
	if err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, LatestKnownChapter: &newChapter, CheckedAt: laterCheck}); err != nil {
		t.Fatalf("update polling state again: %v", err)
	}

	seenAt = chapterSeenAt(t, db, trackerID)
	if seenAt == nil || !seenAt.Equal(discoveredAt) {
		t.Fatalf("expected the first-seen timestamp to survive a poll that found no new chapter, got %v", seenAt)
	}
}

// TestUpdatePollingStateKeepsChapterSeenAtWhenReleaseDateIsKnown records that the
// stamp is written regardless of whether the source dated the chapter: the column
// is only consulted when no release date exists, and a source can stop reporting
// dates between one poll and the next.
func TestUpdatePollingStateKeepsChapterSeenAtWhenReleaseDateIsKnown(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	newChapter := 20.0
	releasedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	discoveredAt := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	if err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, LatestKnownChapter: &newChapter, LatestReleaseAt: &releasedAt, CheckedAt: discoveredAt}); err != nil {
		t.Fatalf("update polling state: %v", err)
	}

	tracker, err := repo.GetByID(1, trackerID)
	if err != nil {
		t.Fatalf("get tracker: %v", err)
	}
	if tracker == nil {
		t.Fatalf("expected the updated tracker to be readable")
	}
	if tracker.LatestReleaseAt == nil || !tracker.LatestReleaseAt.UTC().Equal(releasedAt) {
		t.Fatalf("expected the reported release date to be stored, got %v", tracker.LatestReleaseAt)
	}
	if tracker.LatestChapterSeenAt == nil || !tracker.LatestChapterSeenAt.UTC().Equal(discoveredAt) {
		t.Fatalf("expected the first-seen timestamp alongside it, got %v", tracker.LatestChapterSeenAt)
	}
}
