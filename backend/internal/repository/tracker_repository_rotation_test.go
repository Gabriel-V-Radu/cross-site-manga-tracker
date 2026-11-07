package repository_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// alternateURL is the MangaFire link seedRotationLinks gives the tracker
// beside its MangaDex primary.
const alternateURL = "https://mangafire.to/title/polling"

// seedRotationLinks gives a seeded tracker its primary's mirror row and one
// alternate, the shape every linked tracker has on the deployed database.
func seedRotationLinks(t *testing.T, db *sql.DB, trackerID int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES
			(?, (SELECT id FROM sources WHERE key = 'mangadex'), ?),
			(?, (SELECT id FROM sources WHERE key = 'mangafire'), ?)
	`, trackerID, pollingSeedURL, trackerID, alternateURL); err != nil {
		t.Fatalf("seed links: %v", err)
	}
}

func mangafireID(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&id); err != nil {
		t.Fatalf("look up mangafire: %v", err)
	}
	return id
}

func pollingTracker(t *testing.T, repo *repository.TrackerRepository, trackerID int64) repository.PollingTracker {
	t.Helper()
	trackers, err := repo.ListForPolling(context.Background())
	if err != nil {
		t.Fatalf("list for polling: %v", err)
	}
	for _, tracker := range trackers {
		if tracker.ID == trackerID {
			return tracker
		}
	}
	t.Fatalf("tracker %d not listed", trackerID)
	return repository.PollingTracker{}
}

// The rotation's memory round-trips: a poll's per-site record is read back by
// the next cycle, the primary's through its mirror row, and a failed request
// stamps the site without changing whether it was in step.
func TestPollRotationStateRoundTrips(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)
	seedRotationLinks(t, db, trackerID)
	alternateID := mangafireID(t, db)

	tracker := pollingTracker(t, repo, trackerID)
	if tracker.PrimaryLastPolledAt != nil || len(tracker.AlternateSources) != 1 {
		t.Fatalf("expected an unpolled primary and one alternate, got %+v", tracker)
	}
	if alternate := tracker.AlternateSources[0]; alternate.LastPolledAt != nil || alternate.InStep {
		t.Fatalf("expected an unpolled alternate not in step, got %+v", alternate)
	}

	stored := 19.0
	firstPoll := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	applied, err := repo.UpdatePollingState(context.Background(), repository.PollingUpdate{
		TrackerID: trackerID, SnapshotSourceID: 1, SnapshotSourceURL: pollingSeedURL, SnapshotLatestKnownChapter: &stored,
		LatestKnownChapter: &stored,
		SourcePolls: []repository.SourcePoll{
			{Primary: true, Answered: true, InStep: true},
			{SourceID: alternateID, SourceURL: alternateURL, Answered: true, InStep: true},
		},
		CheckedAt: firstPoll,
	})
	if err != nil || !applied {
		t.Fatalf("update polling state: applied=%v err=%v", applied, err)
	}

	tracker = pollingTracker(t, repo, trackerID)
	if tracker.PrimaryLastPolledAt == nil || !tracker.PrimaryLastPolledAt.Equal(firstPoll) {
		t.Fatalf("expected the primary stamped at %s, got %v", firstPoll, tracker.PrimaryLastPolledAt)
	}
	if alternate := tracker.AlternateSources[0]; alternate.LastPolledAt == nil || !alternate.LastPolledAt.Equal(firstPoll) || !alternate.InStep {
		t.Fatalf("expected the alternate stamped and in step, got %+v", alternate)
	}

	failedPoll := firstPoll.Add(time.Hour)
	if err := repo.MarkPollCheckedAt(context.Background(), trackerID, failedPoll, []repository.SourcePoll{
		{SourceID: alternateID, SourceURL: alternateURL},
	}); err != nil {
		t.Fatalf("mark poll checked: %v", err)
	}
	tracker = pollingTracker(t, repo, trackerID)
	if alternate := tracker.AlternateSources[0]; alternate.LastPolledAt == nil || !alternate.LastPolledAt.Equal(failedPoll) || !alternate.InStep {
		t.Fatalf("a failed request restamps the site and leaves it in step, got %+v", alternate)
	}
	if tracker.LastCheckedAt == nil || !tracker.LastCheckedAt.Equal(failedPoll) {
		t.Fatalf("expected last_checked_at %s, got %v", failedPoll, tracker.LastCheckedAt)
	}
}

// A primary poll that refreshed the canonical URL has moved the primary's
// mirror row; its stamp has to land on the row as it now stands, or the next
// cycle would see the primary as never polled and give it the turn again.
func TestPollRotationStampsThePrimaryOnItsMovedMirrorRow(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)
	seedRotationLinks(t, db, trackerID)

	stored := 19.0
	canonicalURL := "https://mangadex.org/title/polling-canonical"
	polledAt := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	if _, err := repo.UpdatePollingState(context.Background(), repository.PollingUpdate{
		TrackerID: trackerID, SnapshotSourceID: 1, SnapshotSourceURL: pollingSeedURL, SnapshotLatestKnownChapter: &stored,
		SourceID: 1, CurrentSourceURL: pollingSeedURL, SourceURL: canonicalURL,
		LatestKnownChapter: &stored,
		SourcePolls:        []repository.SourcePoll{{SourceID: 1, SourceURL: pollingSeedURL, Primary: true, Answered: true, InStep: true}},
		CheckedAt:          polledAt,
	}); err != nil {
		t.Fatalf("update polling state: %v", err)
	}

	tracker := pollingTracker(t, repo, trackerID)
	if tracker.SourceURL != canonicalURL {
		t.Fatalf("expected the canonical url written, got %q", tracker.SourceURL)
	}
	if tracker.PrimaryLastPolledAt == nil || !tracker.PrimaryLastPolledAt.Equal(polledAt) {
		t.Fatalf("expected the moved mirror row stamped at %s, got %v", polledAt, tracker.PrimaryLastPolledAt)
	}
}

// A link the user removed while the cycle ran stays removed: recording a poll
// only ever updates a link that still exists.
func TestPollRotationNeverRecreatesARemovedLink(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	if err := repo.MarkPollCheckedAt(context.Background(), trackerID, time.Now().UTC(), []repository.SourcePoll{
		{SourceID: mangafireID(t, db), SourceURL: alternateURL, Answered: true, InStep: true},
		{Primary: true, Answered: true},
	}); err != nil {
		t.Fatalf("mark poll checked: %v", err)
	}
	var links int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, trackerID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Fatalf("recording a poll created %d link rows", links)
	}
}
