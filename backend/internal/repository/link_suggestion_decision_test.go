package repository_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// A Link Review decision writes a link row and settles every candidate the
// tracker had on that source. These tests pin that it is all-or-nothing: a
// half-applied decision either loses the link the user just made or leaves the
// queue offering candidates for a series that is already linked.

const (
	bestCandidateURL    = "https://comick.io/comic/01ABCDEFGHJKMNPQRSTVWXYZ01"
	siblingCandidateURL = "https://comick.io/comic/01ABCDEFGHJKMNPQRSTVWXYZ02"
)

func suggestionIDByURL(t *testing.T, db *sql.DB, url string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM source_link_suggestions WHERE candidate_url = ?`, url).Scan(&id); err != nil {
		t.Fatalf("look up suggestion %q: %v", url, err)
	}
	return id
}

func suggestionStatusByURL(t *testing.T, db *sql.DB, url string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM source_link_suggestions WHERE candidate_url = ?`, url).Scan(&status); err != nil {
		t.Fatalf("read status of %q: %v", url, err)
	}
	return status
}

func countLinkRows(t *testing.T, db *sql.DB, trackerID int64, sourceID int64) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = ?
	`, trackerID, sourceID).Scan(&count); err != nil {
		t.Fatalf("count link rows: %v", err)
	}
	return count
}

func seedTwoCandidates(t *testing.T, db *sql.DB, trackerID int64, sourceID int64) {
	t.Helper()
	repo := repository.NewLinkSuggestionRepository(db)
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, sourceID, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, sourceID, bestCandidateURL, "Series", 1.0),
		pendingSuggestion(trackerID, sourceID, siblingCandidateURL, "Series (Colored)", 0.5),
	}); err != nil {
		t.Fatalf("store suggestions: %v", err)
	}
}

func TestAcceptSuggestionAppliesTheWholeDecision(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")
	seedTwoCandidates(t, db, trackerID, comick)

	bestID := suggestionIDByURL(t, db, bestCandidateURL)
	accepted, err := repo.AcceptSuggestion(context.Background(), 1, bestID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted == nil {
		t.Fatal("accept returned no suggestion for a pending candidate")
	}
	if accepted.ID != bestID || accepted.Status != repository.LinkSuggestionAccepted {
		t.Fatalf("accepted = %+v, want id %d accepted", accepted, bestID)
	}

	if got := countLinkRows(t, db, trackerID, comick); got != 1 {
		t.Fatalf("tracker_sources rows = %d, want exactly 1", got)
	}
	var linkedURL string
	if err := db.QueryRow(`
		SELECT source_url FROM tracker_sources WHERE tracker_id = ? AND source_id = ?
	`, trackerID, comick).Scan(&linkedURL); err != nil {
		t.Fatalf("read link row: %v", err)
	}
	if linkedURL != bestCandidateURL {
		t.Fatalf("linked url = %q, want the accepted candidate", linkedURL)
	}

	if got := suggestionStatusByURL(t, db, bestCandidateURL); got != repository.LinkSuggestionAccepted {
		t.Fatalf("accepted candidate status = %q", got)
	}
	if got := suggestionStatusByURL(t, db, siblingCandidateURL); got != repository.LinkSuggestionRejected {
		t.Fatalf("sibling status = %q, want rejected", got)
	}

	queue, err := repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue after accept = %+v, want empty", queue)
	}

	// Double-submitting the same card (or a bulk accept racing the button)
	// finds nothing pending and must not add a second link row.
	again, err := repo.AcceptSuggestion(context.Background(), 1, bestID)
	if err != nil {
		t.Fatalf("re-accept: %v", err)
	}
	if again != nil {
		t.Fatalf("re-accept returned %+v, want nil for an already decided candidate", again)
	}
	if got := countLinkRows(t, db, trackerID, comick); got != 1 {
		t.Fatalf("tracker_sources rows after re-accept = %d, want 1", got)
	}
}

// TestAcceptSuggestionRollsBackEverythingOnFailure forces the last statement of
// the decision to fail. Before the decision became one transaction this left
// the tracker linked and its candidate accepted with the sibling still pending.
func TestAcceptSuggestionRollsBackEverythingOnFailure(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")
	seedTwoCandidates(t, db, trackerID, comick)

	// A trigger body cannot carry bound parameters, hence the inlined URL.
	if _, err := db.Exec(`
		CREATE TRIGGER fail_sibling_rejection
		BEFORE UPDATE OF status ON source_link_suggestions
		WHEN NEW.status = 'rejected' AND OLD.candidate_url = '` + siblingCandidateURL + `'
		BEGIN
			SELECT RAISE(ABORT, 'forced failure');
		END
	`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	bestID := suggestionIDByURL(t, db, bestCandidateURL)
	_, err := repo.AcceptSuggestion(context.Background(), 1, bestID)
	if err == nil {
		t.Fatal("accept succeeded despite the forced failure")
	}
	if !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("accept failed for another reason than the injected one: %v", err)
	}

	if got := countLinkRows(t, db, trackerID, comick); got != 0 {
		t.Fatalf("tracker_sources rows = %d, want none: the link was not rolled back", got)
	}
	if got := suggestionStatusByURL(t, db, bestCandidateURL); got != repository.LinkSuggestionPending {
		t.Fatalf("candidate status = %q, want pending: the accept was not rolled back", got)
	}
	if got := suggestionStatusByURL(t, db, siblingCandidateURL); got != repository.LinkSuggestionPending {
		t.Fatalf("sibling status = %q, want pending", got)
	}

	queue, queueErr := repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if queueErr != nil {
		t.Fatalf("list queue: %v", queueErr)
	}
	if len(queue) != 1 || len(queue[0].Suggestions) != 2 {
		t.Fatalf("queue after the failed accept = %+v, want the tracker with both candidates", queue)
	}
}

// TestApplyManualLinkSettlesTheReview covers the paste-a-URL fallback pointing
// at another site: the tracker is linked there and dismissed for the source
// under review, so it leaves that queue instead of being offered again.
func TestApplyManualLinkSettlesTheReview(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	mangadex := sourceIDByKey(t, db, "mangadex")
	trackerID := seedLinkTracker(t, db, "series")
	seedTwoCandidates(t, db, trackerID, comick)

	if err := repo.ApplyManualLink(context.Background(), 1, trackerID, models.TrackerSource{
		SourceID:  mangadex,
		SourceURL: "https://mangadex.org/title/abc",
	}, comick); err != nil {
		t.Fatalf("apply manual link: %v", err)
	}

	if got := countLinkRows(t, db, trackerID, mangadex); got != 1 {
		t.Fatalf("mangadex link rows = %d, want 1", got)
	}
	if got := suggestionStatusByURL(t, db, bestCandidateURL); got != repository.LinkSuggestionRejected {
		t.Fatalf("candidate status = %q, want rejected", got)
	}
	targets, err := repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list scan targets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("scan targets after the manual link = %+v, want empty", targets)
	}

	// Another profile's tracker is not reachable through the same call.
	other := seedLinkTracker(t, db, "other")
	if _, err := db.Exec(`UPDATE trackers SET profile_id = 2 WHERE id = ?`, other); err != nil {
		t.Fatalf("move tracker to another profile: %v", err)
	}
	if err := repo.ApplyManualLink(context.Background(), 1, other, models.TrackerSource{
		SourceID:  mangadex,
		SourceURL: "https://mangadex.org/title/def",
	}, comick); err != nil {
		t.Fatalf("apply manual link for a foreign tracker: %v", err)
	}
	if got := countLinkRows(t, db, other, mangadex); got != 0 {
		t.Fatalf("foreign tracker gained %d link rows", got)
	}
}

// TestApplyManualLinkRollsBackEverythingOnFailure forces the dismissal marker
// to fail: the link must not survive on its own, or the tracker stays in the
// queue with live candidates for a series it is already linked to elsewhere.
func TestApplyManualLinkRollsBackEverythingOnFailure(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	mangadex := sourceIDByKey(t, db, "mangadex")
	trackerID := seedLinkTracker(t, db, "series")
	seedTwoCandidates(t, db, trackerID, comick)

	if _, err := db.Exec(`
		CREATE TRIGGER fail_dismissal_marker
		BEFORE INSERT ON source_link_suggestions
		WHEN NEW.status = 'dismissed'
		BEGIN
			SELECT RAISE(ABORT, 'forced failure');
		END
	`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	err := repo.ApplyManualLink(context.Background(), 1, trackerID, models.TrackerSource{
		SourceID:  mangadex,
		SourceURL: "https://mangadex.org/title/abc",
	}, comick)
	if err == nil {
		t.Fatal("manual link succeeded despite the forced failure")
	}
	if !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("manual link failed for another reason than the injected one: %v", err)
	}

	if got := countLinkRows(t, db, trackerID, mangadex); got != 0 {
		t.Fatalf("mangadex link rows = %d, want none: the link was not rolled back", got)
	}
	if got := suggestionStatusByURL(t, db, bestCandidateURL); got != repository.LinkSuggestionPending {
		t.Fatalf("candidate status = %q, want pending", got)
	}
}

// TestDismissTrackerRollsBackEverythingOnFailure pins the same guarantee for
// the dismiss button: a rejected candidate with no marker row would make the
// next scan offer the tracker again with nothing to show for it.
func TestDismissTrackerRollsBackEverythingOnFailure(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")
	seedTwoCandidates(t, db, trackerID, comick)

	if _, err := db.Exec(`
		CREATE TRIGGER fail_dismissal_marker
		BEFORE INSERT ON source_link_suggestions
		WHEN NEW.status = 'dismissed'
		BEGIN
			SELECT RAISE(ABORT, 'forced failure');
		END
	`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	if err := repo.DismissTracker(context.Background(), 1, trackerID, comick); err == nil {
		t.Fatal("dismiss succeeded despite the forced failure")
	}
	if got := suggestionStatusByURL(t, db, bestCandidateURL); got != repository.LinkSuggestionPending {
		t.Fatalf("candidate status = %q, want pending", got)
	}
	if got := suggestionStatusByURL(t, db, siblingCandidateURL); got != repository.LinkSuggestionPending {
		t.Fatalf("sibling status = %q, want pending", got)
	}
}
