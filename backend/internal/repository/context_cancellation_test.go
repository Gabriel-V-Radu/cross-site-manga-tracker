package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// The pool runs MaxOpenConns(1). A transaction that is never committed or
// rolled back holds that one connection, and the context-free database/sql
// calls queue behind it with no deadline — the process stops working with no
// error and no log line. These tests pin that the context every method takes
// turns that wait into an error a caller can act on, and that a transaction
// aborted that way leaves nothing half-written.

// cancellationCeiling bounds every wait below. What is under test here IS a
// hang, so no assertion may block on an unbounded receive: a regression has to
// fail the test, not freeze the package run.
const cancellationCeiling = 15 * time.Second

// runBounded runs fn on its own goroutine and fails the test if it has not
// returned within the ceiling.
func runBounded(t *testing.T, fn func() error) error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err
	case <-time.After(cancellationCeiling):
		t.Fatalf("the call never returned within %s: it hung instead of failing", cancellationCeiling)
		return nil
	}
}

// TestContextDeadlineFreesACallerQueuedBehindAnOpenTransaction is the failure
// this whole change exists for: an open transaction owns the pool's only
// connection, so anything else has to wait for it. A caller with a deadline
// gives up and says so; the same call without a context would wait forever.
func TestContextDeadlineFreesACallerQueuedBehindAnOpenTransaction(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	blocker, err := db.Begin()
	if err != nil {
		t.Fatalf("open the blocking transaction: %v", err)
	}
	defer blocker.Rollback()

	probes := []struct {
		name string
		call func(context.Context) error
	}{
		{"read", func(ctx context.Context) error {
			_, err := repo.ListForPolling(ctx)
			return err
		}},
		{"write", func(ctx context.Context) error {
			return repo.MarkPollCheckedAt(ctx, trackerID, time.Now().UTC())
		}},
	}

	for _, probe := range probes {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := runBounded(t, func() error { return probe.call(ctx) })
		cancel()

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: expected the queued call to give up on its deadline, got %v", probe.name, err)
		}
	}
}

// TestUpdatePollingStateAbortsOnACancelledContext covers the poller's
// write: a cancelled cycle must surface an error rather than begin a
// transaction nobody will finish.
func TestUpdatePollingStateAbortsOnACancelledContext(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	newChapter := 20.0
	err := runBounded(t, func() error {
		_, err := repo.UpdatePollingState(ctx, repository.PollingUpdate{
			TrackerID:          trackerID,
			SnapshotSourceID:   1,
			LatestKnownChapter: &newChapter,
			CheckedAt:          time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
		})
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancelled context to surface as an error, got %v", err)
	}

	var stored sql.NullFloat64
	if err := db.QueryRow(`SELECT latest_known_chapter FROM trackers WHERE id = ?`, trackerID).Scan(&stored); err != nil {
		t.Fatalf("read stored chapter: %v", err)
	}
	if !stored.Valid || stored.Float64 != 19 {
		t.Fatalf("expected the aborted write to leave the stored chapter alone, got %#v", stored)
	}
}

// TestAcceptSuggestionLeavesNothingHalfApplied pins that cancellation
// does not turn a multi-statement decision into a partial one: no link row
// without the accepted status, no accepted status without the link.
func TestAcceptSuggestionLeavesNothingHalfApplied(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	trackerID := seedLinkTracker(t, db, "cancelled-accept")
	comick := sourceIDByKey(t, db, "comick")

	candidateURL := "https://comick.dev/comic/cancelled-accept"
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, candidateURL, "Cancelled Accept", 1.0),
	}); err != nil {
		t.Fatalf("store the candidate: %v", err)
	}
	suggestionID := suggestionIDByURL(t, db, candidateURL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runBounded(t, func() error {
		_, err := repo.AcceptSuggestion(ctx, 1, suggestionID)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancelled context to surface as an error, got %v", err)
	}

	if linked := countLinkRows(t, db, trackerID, comick); linked != 0 {
		t.Fatalf("expected the aborted accept to write no link row, got %d", linked)
	}
	if status := suggestionStatusByURL(t, db, candidateURL); status != repository.LinkSuggestionPending {
		t.Fatalf("expected the candidate to stay pending, got %q", status)
	}
}

// TestReplaceTrackerTagsStoresOnlyTheProfilesOwnTags guards the ownership
// filter now that the tag lookup drains its cursor in a helper: the inserts
// that follow run on the transaction's connection, which that cursor holds
// until it is closed.
func TestReplaceTrackerTagsStoresOnlyTheProfilesOwnTags(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 5)

	mine, err := repo.CreateProfileTag(context.Background(), 1, "Mine", nil)
	if err != nil {
		t.Fatalf("create the profile's own tag: %v", err)
	}
	theirs, err := repo.CreateProfileTag(context.Background(), 2, "Theirs", nil)
	if err != nil {
		t.Fatalf("create the other profile's tag: %v", err)
	}

	if err := repo.ReplaceTrackerTags(context.Background(), 1, trackerID, []int64{mine.ID, theirs.ID}); err != nil {
		t.Fatalf("replace tracker tags: %v", err)
	}

	var linked int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_tags WHERE tracker_id = ?`, trackerID).Scan(&linked); err != nil {
		t.Fatalf("count linked tags: %v", err)
	}
	if linked != 1 {
		t.Fatalf("expected only the profile's own tag to be linked, got %d rows", linked)
	}

	var linkedTagID int64
	if err := db.QueryRow(`SELECT tag_id FROM tracker_tags WHERE tracker_id = ?`, trackerID).Scan(&linkedTagID); err != nil {
		t.Fatalf("read linked tag: %v", err)
	}
	if linkedTagID != mine.ID {
		t.Fatalf("expected tag %d to be the linked one, got %d", mine.ID, linkedTagID)
	}

	if err := repo.ReplaceTrackerTags(context.Background(), 1, trackerID, nil); err != nil {
		t.Fatalf("clear tracker tags: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_tags WHERE tracker_id = ?`, trackerID).Scan(&linked); err != nil {
		t.Fatalf("recount linked tags: %v", err)
	}
	if linked != 0 {
		t.Fatalf("expected the replace to clear the tracker's tags, got %d rows", linked)
	}
}

// TestListReturnsTrackersWithTheirTags covers the second half of the
// cursor rule: the tag query runs only once the tracker cursor has been
// drained into a slice, so both statements get the single connection in turn.
func TestListReturnsTrackersWithTheirTags(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 5)

	tag, err := repo.CreateProfileTag(context.Background(), 1, "Favourites", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if err := repo.ReplaceTrackerTags(context.Background(), 1, trackerID, []int64{tag.ID}); err != nil {
		t.Fatalf("link tag: %v", err)
	}

	trackers, err := repo.List(context.Background(), repository.TrackerListOptions{ProfileID: 1})
	if err != nil {
		t.Fatalf("list trackers: %v", err)
	}
	if len(trackers) != 1 {
		t.Fatalf("expected the seeded tracker back, got %d", len(trackers))
	}
	if len(trackers[0].Tags) != 1 || trackers[0].Tags[0].Name != "Favourites" {
		t.Fatalf("expected the tracker's tag alongside it, got %+v", trackers[0].Tags)
	}
}
