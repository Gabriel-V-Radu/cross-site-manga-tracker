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
	if _, err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, SnapshotSourceID: 1, LatestKnownChapter: &newChapter, ClearLatestReleaseAt: true, CheckedAt: discoveredAt}); err != nil {
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
	if _, err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, SnapshotSourceID: 1, LatestKnownChapter: &newChapter, CheckedAt: laterCheck}); err != nil {
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
	if _, err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, SnapshotSourceID: 1, LatestKnownChapter: &newChapter, LatestReleaseAt: &releasedAt, CheckedAt: discoveredAt}); err != nil {
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

// TestUpdatePollingStateRecordsAndKeepsChapterReporter covers the reporter
// column's two writes: a poll that names a reporter records it, and a later
// poll that names none (its report lost the reconciliation) leaves the
// recorded one in place rather than erasing the attribution.
func TestUpdatePollingStateRecordsAndKeepsChapterReporter(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	newChapter := 20.0
	reporter := int64(3)
	checkedAt := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	if _, err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, SnapshotSourceID: 1, LatestKnownChapter: &newChapter, LatestChapterSourceID: &reporter, CheckedAt: checkedAt}); err != nil {
		t.Fatalf("update polling state: %v", err)
	}

	tracker, err := repo.GetByID(1, trackerID)
	if err != nil || tracker == nil {
		t.Fatalf("get tracker: %v", err)
	}
	if tracker.LatestChapterSourceID == nil || *tracker.LatestChapterSourceID != reporter {
		t.Fatalf("expected reporter source %d to be recorded, got %#v", reporter, tracker.LatestChapterSourceID)
	}

	if _, err := repo.UpdatePollingState(repository.PollingUpdate{TrackerID: trackerID, SnapshotSourceID: 1, LatestKnownChapter: &newChapter, CheckedAt: checkedAt.Add(time.Hour)}); err != nil {
		t.Fatalf("update polling state again: %v", err)
	}

	tracker, err = repo.GetByID(1, trackerID)
	if err != nil || tracker == nil {
		t.Fatalf("get tracker again: %v", err)
	}
	if tracker.LatestChapterSourceID == nil || *tracker.LatestChapterSourceID != reporter {
		t.Fatalf("expected the recorded reporter to survive a poll that named none, got %#v", tracker.LatestChapterSourceID)
	}
}

// TestUpdatePollingStateSkipsWhenSourceChangedMidCycle covers the optimistic
// guard: a poll cycle can run for tens of minutes off a snapshot taken at its
// start, and a user who repoints the tracker's source mid-cycle must not have
// that edit reverted by the stale write — which could also leave source_id
// (new) mismatched with source_url (old), a state host validation rejects
// forever.
func TestUpdatePollingStateSkipsWhenSourceChangedMidCycle(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	// The user repoints the tracker at source 2 after the poll snapshotted
	// source 1.
	userURL := "https://mangafire.to/manga/repointed"
	if _, err := db.Exec(`UPDATE trackers SET source_id = 2, source_url = ?, latest_known_chapter = 25 WHERE id = ?`, userURL, trackerID); err != nil {
		t.Fatalf("simulate user edit: %v", err)
	}

	staleChapter := 20.0
	applied, err := repo.UpdatePollingState(repository.PollingUpdate{
		TrackerID:          trackerID,
		SnapshotSourceID:   1,
		SourceID:           1,
		CurrentSourceURL:   "https://mangadex.org/title/polling",
		SourceURL:          "https://mangadex.org/title/polling-canonical",
		LatestKnownChapter: &staleChapter,
		CheckedAt:          time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("update polling state: %v", err)
	}
	if applied {
		t.Fatalf("expected the stale write to be skipped after the mid-cycle edit")
	}

	var sourceID int64
	var sourceURL string
	var latest sql.NullFloat64
	if err := db.QueryRow(`SELECT source_id, source_url, latest_known_chapter FROM trackers WHERE id = ?`, trackerID).Scan(&sourceID, &sourceURL, &latest); err != nil {
		t.Fatalf("read tracker after skipped write: %v", err)
	}
	if sourceID != 2 || sourceURL != userURL {
		t.Fatalf("expected the user's repoint to survive, got source_id=%d source_url=%q", sourceID, sourceURL)
	}
	if !latest.Valid || latest.Float64 != 25 {
		t.Fatalf("expected the user's chapter correction to survive, got %#v", latest)
	}

	// The dependent mirror statements must be skipped along with the UPDATE:
	// the stale snapshot's canonical URL has no business in tracker_sources.
	var mirrored int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = 1`, trackerID).Scan(&mirrored); err != nil {
		t.Fatalf("count mirror rows: %v", err)
	}
	if mirrored != 0 {
		t.Fatalf("expected no mirror row from the skipped write, got %d", mirrored)
	}
}

// TestUpdatePollingStateUpsertsMirrorRow proves the tracker_sources mirror
// still lands now that the whole operation runs in one transaction, including
// the stale-URL cleanup when the canonical URL moved.
func TestUpdatePollingStateUpsertsMirrorRow(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	oldURL := "https://mangadex.org/title/polling"
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url)
		VALUES (?, 1, ?)
	`, trackerID, oldURL); err != nil {
		t.Fatalf("seed stale mirror row: %v", err)
	}

	newChapter := 20.0
	itemID := "canonical-item"
	newURL := "https://mangadex.org/title/polling-canonical"
	applied, err := repo.UpdatePollingState(repository.PollingUpdate{
		TrackerID:          trackerID,
		SnapshotSourceID:   1,
		SourceID:           1,
		CurrentSourceURL:   oldURL,
		SourceItemID:       &itemID,
		SourceURL:          newURL,
		LatestKnownChapter: &newChapter,
		CheckedAt:          time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("update polling state: %v", err)
	}
	if !applied {
		t.Fatalf("expected the write to apply against an unchanged tracker")
	}

	rows, err := db.Query(`SELECT source_url, source_item_id FROM tracker_sources WHERE tracker_id = ? AND source_id = 1`, trackerID)
	if err != nil {
		t.Fatalf("read mirror rows: %v", err)
	}
	defer rows.Close()

	mirrors := map[string]string{}
	for rows.Next() {
		var url string
		var item sql.NullString
		if err := rows.Scan(&url, &item); err != nil {
			t.Fatalf("scan mirror row: %v", err)
		}
		mirrors[url] = item.String
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate mirror rows: %v", err)
	}

	if _, stale := mirrors[oldURL]; stale {
		t.Fatalf("expected the stale mirror row to be deleted, still present: %v", mirrors)
	}
	if item, ok := mirrors[newURL]; !ok || item != itemID {
		t.Fatalf("expected the canonical mirror row with item id %q, got %v", itemID, mirrors)
	}
	if len(mirrors) != 1 {
		t.Fatalf("expected exactly the canonical mirror row, got %v", mirrors)
	}
}
