package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// The feeds' miss is remembered on the tracker: a poll that says so stamps
// feed_missed_at, the next cycle reads it back, and a later poll that does not
// say so leaves the stamp where it was.
func TestFeedMissedStampRoundTrips(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	if tracker := pollingTracker(t, repo, trackerID); tracker.FeedMissedAt != nil {
		t.Fatalf("a tracker the feeds never failed has no stamp, got %v", tracker.FeedMissedAt)
	}

	stored, next := 19.0, 20.0
	missedAt := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	applied, err := repo.UpdatePollingState(context.Background(), repository.PollingUpdate{
		TrackerID: trackerID, SnapshotSourceID: 1, SnapshotSourceURL: pollingSeedURL, SnapshotLatestKnownChapter: &stored,
		LatestKnownChapter: &next,
		FeedMissed:         true,
		CheckedAt:          missedAt,
	})
	if err != nil || !applied {
		t.Fatalf("update polling state: applied=%v err=%v", applied, err)
	}
	tracker := pollingTracker(t, repo, trackerID)
	if tracker.FeedMissedAt == nil || !tracker.FeedMissedAt.Equal(missedAt) {
		t.Fatalf("expected the miss stamped at %s, got %v", missedAt, tracker.FeedMissedAt)
	}

	applied, err = repo.UpdatePollingState(context.Background(), repository.PollingUpdate{
		TrackerID: trackerID, SnapshotSourceID: 1, SnapshotSourceURL: pollingSeedURL, SnapshotLatestKnownChapter: &next,
		LatestKnownChapter: &next,
		CheckedAt:          missedAt.Add(time.Hour),
	})
	if err != nil || !applied {
		t.Fatalf("update polling state: applied=%v err=%v", applied, err)
	}
	tracker = pollingTracker(t, repo, trackerID)
	if tracker.FeedMissedAt == nil || !tracker.FeedMissedAt.Equal(missedAt) {
		t.Fatalf("a poll that reports no miss must leave the stamp alone, got %v", tracker.FeedMissedAt)
	}
}
