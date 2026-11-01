package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func openLinkSuggestionTestDB(t *testing.T) *sql.DB {
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

func sourceIDByKey(t *testing.T, db *sql.DB, key string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, key).Scan(&id); err != nil {
		t.Fatalf("look up source %q: %v", key, err)
	}
	return id
}

func seedLinkTracker(t *testing.T, db *sql.DB, title string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (1, ?, ?, ?, 'reading', 100)
	`, title, sourceIDByKey(t, db, "mangafire"), "https://mangafire.to/manga/"+title)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seeded tracker id: %v", err)
	}
	return id
}

func pendingSuggestion(trackerID, sourceID int64, url, title string, score float64) repository.LinkSuggestion {
	return repository.LinkSuggestion{
		TrackerID:      trackerID,
		SourceID:       sourceID,
		CandidateURL:   url,
		CandidateTitle: title,
		Score:          score,
	}
}

func TestScanTargetsExcludeLinkedAndDecidedTrackers(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	trackerRepo := repository.NewTrackerRepository(db)
	comick := sourceIDByKey(t, db, "comick")

	unlinked := seedLinkTracker(t, db, "unlinked")
	linked := seedLinkTracker(t, db, "linked")
	dismissed := seedLinkTracker(t, db, "dismissed")

	if err := trackerRepo.UpsertTrackerSource(context.Background(), 1, linked, models.TrackerSource{
		SourceID:  comick,
		SourceURL: "https://comick.io/comic/01ABCDEFGHJKMNPQRSTVWXYZ01",
	}); err != nil {
		t.Fatalf("link tracker: %v", err)
	}
	if err := repo.DismissTracker(context.Background(), 1, dismissed, comick); err != nil {
		t.Fatalf("dismiss tracker: %v", err)
	}

	targets, err := repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list scan targets: %v", err)
	}
	if len(targets) != 1 || targets[0].TrackerID != unlinked {
		t.Fatalf("targets = %+v, want only the unlinked tracker %d", targets, unlinked)
	}
}

// TestScanFilterNarrowsTargets pins the scope controls: status, primary
// source, and how many alternates a tracker already has.
func TestScanFilterNarrowsTargets(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	trackerRepo := repository.NewTrackerRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	mangafire := sourceIDByKey(t, db, "mangafire")
	mangadex := sourceIDByKey(t, db, "mangadex")
	mangahub := sourceIDByKey(t, db, "mangahub")

	reading := seedLinkTracker(t, db, "reading-no-alternates")

	planned := seedLinkTracker(t, db, "planned")
	if _, err := db.Exec(`UPDATE trackers SET status = 'plan_to_read' WHERE id = ?`, planned); err != nil {
		t.Fatalf("set status: %v", err)
	}

	onMangadex := seedLinkTracker(t, db, "on-mangadex")
	if _, err := db.Exec(`UPDATE trackers SET source_id = ? WHERE id = ?`, mangadex, onMangadex); err != nil {
		t.Fatalf("repoint primary: %v", err)
	}

	withAlternate := seedLinkTracker(t, db, "with-alternate")
	if err := trackerRepo.UpsertTrackerSource(context.Background(), 1, withAlternate, models.TrackerSource{
		SourceID:  mangahub,
		SourceURL: "https://mangahub.io/manga/with-alternate.xx",
	}); err != nil {
		t.Fatalf("link alternate: %v", err)
	}

	// Status: reading + plan_to_read leaves out nothing here except nothing;
	// reading alone leaves out the planned tracker.
	targets, err := repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{Statuses: []string{"reading"}})
	if err != nil {
		t.Fatalf("status filter: %v", err)
	}
	for _, target := range targets {
		if target.TrackerID == planned {
			t.Fatalf("status filter leaked the plan_to_read tracker: %+v", targets)
		}
	}

	// Primary source: only the mangafire ones.
	targets, err = repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{PrimarySourceIDs: []int64{mangafire}})
	if err != nil {
		t.Fatalf("primary filter: %v", err)
	}
	for _, target := range targets {
		if target.TrackerID == onMangadex {
			t.Fatalf("primary filter leaked the mangadex tracker: %+v", targets)
		}
	}

	// A non-nil empty primary set ("broken" resolved to none) matches nothing.
	targets, err = repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{PrimarySourceIDs: []int64{}})
	if err != nil {
		t.Fatalf("empty primary filter: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("empty primary set matched %+v, want nothing", targets)
	}

	// Max alternates 0: the tracker that already has a fallback drops out.
	zero := 0
	targets, err = repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{MaxAlternates: &zero})
	if err != nil {
		t.Fatalf("alternates filter: %v", err)
	}
	found := map[int64]bool{}
	for _, target := range targets {
		found[target.TrackerID] = true
	}
	if found[withAlternate] {
		t.Fatalf("alternates filter leaked a tracker with a fallback: %+v", targets)
	}
	if !found[reading] {
		t.Fatalf("alternates filter dropped a tracker without fallbacks: %+v", targets)
	}
}

func TestRejectedCandidateDoesNotResurface(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")

	candidate := pendingSuggestion(trackerID, comick, "https://comick.io/comic/A", "Series", 1.0)
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{candidate}); err != nil {
		t.Fatalf("store suggestion: %v", err)
	}

	queue, err := repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	if len(queue) != 1 || len(queue[0].Suggestions) != 1 {
		t.Fatalf("queue = %+v, want one tracker with one suggestion", queue)
	}

	if err := repo.DecideSuggestion(context.Background(), 1, queue[0].Suggestions[0].ID, repository.LinkSuggestionRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// A re-scan reporting the same candidate must not resurrect it.
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{candidate}); err != nil {
		t.Fatalf("re-store suggestion: %v", err)
	}
	queue, err = repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue after rescan: %v", err)
	}
	if len(queue) != 1 || len(queue[0].Suggestions) != 0 {
		t.Fatalf("queue after rescan = %+v, want the tracker with no pending suggestions", queue)
	}
}

func TestAcceptedSuggestionRemovesTrackerFromQueueOnceLinked(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	trackerRepo := repository.NewTrackerRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")

	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, "https://comick.io/comic/A", "Series", 1.0),
		pendingSuggestion(trackerID, comick, "https://comick.io/comic/B", "Series (Colored)", 0.5),
	}); err != nil {
		t.Fatalf("store suggestions: %v", err)
	}

	queue, err := repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	best := queue[0].Suggestions[0]
	if best.Score < 0.999 {
		t.Fatalf("expected best-first ordering, got %+v", queue[0].Suggestions)
	}

	if err := repo.DecideSuggestion(context.Background(), 1, best.ID, repository.LinkSuggestionAccepted); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := trackerRepo.UpsertTrackerSource(context.Background(), 1, trackerID, models.TrackerSource{
		SourceID:  comick,
		SourceURL: best.CandidateURL,
	}); err != nil {
		t.Fatalf("link tracker: %v", err)
	}

	queue, err = repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue after accept: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue after accept = %+v, want empty", queue)
	}

	targets, err := repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list scan targets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("scan targets after accept = %+v, want empty", targets)
	}
}

// TestRescanKeepsSurvivingCandidateIDs pins that a rescan updates a candidate
// it reports again in place: the review cards on screen post that row's id, so
// a fresh id per scan turned every click on an already-rescanned card into a
// "not found".
func TestRescanKeepsSurvivingCandidateIDs(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")

	const (
		survivorURL = "https://comick.io/comic/survivor"
		droppedURL  = "https://comick.io/comic/dropped"
		newURL      = "https://comick.io/comic/new"
		rejectedURL = "https://comick.io/comic/rejected"
	)
	readRow := func(url string) (id int64, title string, score float64, status string, found bool) {
		t.Helper()
		err := db.QueryRow(`
			SELECT id, candidate_title, score, status FROM source_link_suggestions
			WHERE tracker_id = ? AND source_id = ? AND candidate_url = ?
		`, trackerID, comick, url).Scan(&id, &title, &score, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", 0, "", false
		}
		if err != nil {
			t.Fatalf("read suggestion %q: %v", url, err)
		}
		return id, title, score, status, true
	}

	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, survivorURL, "Old Title", 0.6),
		pendingSuggestion(trackerID, comick, droppedURL, "Dropped", 0.5),
		pendingSuggestion(trackerID, comick, rejectedURL, "Rejected", 0.4),
	}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	survivorID, _, _, _, _ := readRow(survivorURL)
	rejectedID, _, _, _, _ := readRow(rejectedURL)
	if err := repo.DecideSuggestion(context.Background(), 1, rejectedID, repository.LinkSuggestionRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, survivorURL, "New Title", 0.9),
		pendingSuggestion(trackerID, comick, newURL, "New", 0.7),
		pendingSuggestion(trackerID, comick, rejectedURL, "Rejected Again", 0.8),
	}); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	id, title, score, status, found := readRow(survivorURL)
	if !found || id != survivorID {
		t.Fatalf("surviving candidate id = %d (found %v), want the original %d", id, found, survivorID)
	}
	if title != "New Title" || score != 0.9 || status != repository.LinkSuggestionPending {
		t.Fatalf("surviving candidate = %q %v %q, want the rescan's title and score, still pending", title, score, status)
	}
	if _, _, _, _, found := readRow(droppedURL); found {
		t.Fatal("a candidate the rescan no longer reports is still stored")
	}
	if _, _, _, status, found := readRow(newURL); !found || status != repository.LinkSuggestionPending {
		t.Fatalf("new candidate found=%v status=%q, want a pending row", found, status)
	}
	if _, title, _, status, _ := readRow(rejectedURL); status != repository.LinkSuggestionRejected || title != "Rejected" {
		t.Fatalf("rejected candidate = %q %q, want it left rejected and untouched", title, status)
	}

	// A rescan that finds nothing clears every pending candidate and still
	// leaves the decided one alone.
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, nil); err != nil {
		t.Fatalf("empty rescan: %v", err)
	}
	var pending int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM source_link_suggestions
		WHERE tracker_id = ? AND source_id = ? AND status = 'pending'
	`, trackerID, comick).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending candidates after an empty rescan = %d, want 0", pending)
	}
	if _, _, _, status, found := readRow(rejectedURL); !found || status != repository.LinkSuggestionRejected {
		t.Fatalf("an empty rescan touched the rejected candidate: found=%v status=%q", found, status)
	}
}

// TestUnlinkedAcceptedTrackerReturnsToReview covers a wrong match accepted in
// Link Review and then unlinked in the edit form: the tracker must come back to
// that source's scan and queue, while the accepted URL stays out of the
// candidates.
func TestUnlinkedAcceptedTrackerReturnsToReview(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")

	const (
		wrongURL   = "https://comick.io/comic/wrong"
		siblingURL = "https://comick.io/comic/sibling"
		freshURL   = "https://comick.io/comic/fresh"
	)
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, wrongURL, "Series", 1.0),
		pendingSuggestion(trackerID, comick, siblingURL, "Series Side Story", 0.6),
	}); err != nil {
		t.Fatalf("store suggestions: %v", err)
	}
	var wrongID int64
	if err := db.QueryRow(`SELECT id FROM source_link_suggestions WHERE candidate_url = ?`, wrongURL).Scan(&wrongID); err != nil {
		t.Fatalf("look up candidate: %v", err)
	}
	if accepted, err := repo.AcceptSuggestion(context.Background(), 1, wrongID); err != nil || accepted == nil {
		t.Fatalf("accept: %+v err=%v", accepted, err)
	}

	// What saving the edit form without that link does to tracker_sources.
	if _, err := db.Exec(`DELETE FROM tracker_sources WHERE tracker_id = ? AND source_id = ?`, trackerID, comick); err != nil {
		t.Fatalf("unlink: %v", err)
	}

	targets, err := repo.ListScanTargets(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list scan targets: %v", err)
	}
	if len(targets) != 1 || targets[0].TrackerID != trackerID {
		t.Fatalf("scan targets after unlinking = %+v, want the tracker back", targets)
	}
	queue, err := repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	if len(queue) != 1 || queue[0].TrackerID != trackerID || len(queue[0].Suggestions) != 0 {
		t.Fatalf("queue after unlinking = %+v, want the tracker with no candidates", queue)
	}

	// The next scan reports the wrong URL again next to a new one: only the
	// new one becomes a candidate.
	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, wrongURL, "Series", 1.0),
		pendingSuggestion(trackerID, comick, freshURL, "Series", 0.9),
	}); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	queue, err = repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue after rescan: %v", err)
	}
	if len(queue) != 1 || len(queue[0].Suggestions) != 1 || queue[0].Suggestions[0].CandidateURL != freshURL {
		t.Fatalf("queue after rescan = %+v, want only the fresh candidate", queue)
	}

	decided, err := repo.DecidedCandidateURLs(context.Background(), trackerID, comick)
	if err != nil {
		t.Fatalf("decided urls: %v", err)
	}
	got := map[string]bool{}
	for _, url := range decided {
		got[url] = true
	}
	if len(decided) != 2 || !got[wrongURL] || !got[siblingURL] {
		t.Fatalf("decided urls = %v, want the accepted and the rejected candidate only", decided)
	}
}

func TestMergeRelatedTitlesUnionsAndFilters(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	trackerID := seedLinkTracker(t, db, "series")

	if _, err := db.Exec(`UPDATE trackers SET related_titles = '["Existing Name"]' WHERE id = ?`, trackerID); err != nil {
		t.Fatalf("seed related titles: %v", err)
	}

	nonLatin := "나노마신" // Korean, must be filtered out
	if err := repo.MergeRelatedTitles(context.Background(), trackerID, []string{
		"Existing Name", // duplicate, must not double up
		"New Alt Name",  // survives
		nonLatin,
		"  ", // empty, filtered
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	var raw string
	if err := db.QueryRow(`SELECT related_titles FROM trackers WHERE id = ?`, trackerID).Scan(&raw); err != nil {
		t.Fatalf("read merged titles: %v", err)
	}
	if !strings.Contains(raw, "Existing Name") || !strings.Contains(raw, "New Alt Name") {
		t.Fatalf("merged titles = %s", raw)
	}
	if strings.Contains(raw, nonLatin) {
		t.Fatalf("non-Latin title leaked: %s", raw)
	}
	if strings.Count(raw, "Existing Name") != 1 {
		t.Fatalf("duplicate not collapsed: %s", raw)
	}
}

func TestQueueScopedToProfile(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	trackerID := seedLinkTracker(t, db, "series")

	if err := repo.ReplacePendingSuggestions(context.Background(), trackerID, comick, []repository.LinkSuggestion{
		pendingSuggestion(trackerID, comick, "https://comick.io/comic/A", "Series", 1.0),
	}); err != nil {
		t.Fatalf("store suggestion: %v", err)
	}

	queue, err := repo.ListReviewQueue(context.Background(), 2, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue for other profile: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("other profile sees %d queue entries, want 0", len(queue))
	}

	suggestionQueue, err := repo.ListReviewQueue(context.Background(), 1, comick, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list queue: %v", err)
	}
	suggestionID := suggestionQueue[0].Suggestions[0].ID

	if got, err := repo.GetPendingSuggestion(context.Background(), 2, suggestionID); err != nil || got != nil {
		t.Fatalf("other profile can read suggestion: %+v err=%v", got, err)
	}
	if err := repo.DecideSuggestion(context.Background(), 2, suggestionID, repository.LinkSuggestionRejected); err != nil {
		t.Fatalf("decide as other profile: %v", err)
	}
	if got, err := repo.GetPendingSuggestion(context.Background(), 1, suggestionID); err != nil || got == nil {
		t.Fatalf("other profile's decision leaked: got=%+v err=%v", got, err)
	}
}

// TestSetReadingSourceValidatesLinkage pins that the reading pin only accepts
// sources the tracker actually carries, and that anything else clears it.
func TestSetReadingSourceValidatesLinkage(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	trackerRepo := repository.NewTrackerRepository(db)
	comick := sourceIDByKey(t, db, "comick")
	mangadex := sourceIDByKey(t, db, "mangadex")
	trackerID := seedLinkTracker(t, db, "series")

	if err := trackerRepo.UpsertTrackerSource(context.Background(), 1, trackerID, models.TrackerSource{
		SourceID:  comick,
		SourceURL: "https://comick.io/comic/01ABCDEFGHJKMNPQRSTVWXYZ01",
	}); err != nil {
		t.Fatalf("link source: %v", err)
	}

	readPin := func() any {
		var value any
		if err := db.QueryRow(`SELECT reading_source_id FROM trackers WHERE id = ?`, trackerID).Scan(&value); err != nil {
			t.Fatalf("read pin: %v", err)
		}
		return value
	}

	// A linked source pins.
	if err := trackerRepo.SetReadingSource(context.Background(), 1, trackerID, &comick); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	if got := readPin(); got != comick {
		t.Fatalf("pin = %v, want %d", got, comick)
	}

	// A source the tracker does not carry clears instead of dangling.
	if err := trackerRepo.SetReadingSource(context.Background(), 1, trackerID, &mangadex); err != nil {
		t.Fatalf("set invalid pin: %v", err)
	}
	if got := readPin(); got != nil {
		t.Fatalf("invalid pin stored: %v", got)
	}

	// The primary source also counts as linked.
	mangafire := sourceIDByKey(t, db, "mangafire")
	if err := trackerRepo.SetReadingSource(context.Background(), 1, trackerID, &mangafire); err != nil {
		t.Fatalf("set primary pin: %v", err)
	}
	if got := readPin(); got != mangafire {
		t.Fatalf("primary pin = %v, want %d", got, mangafire)
	}

	// Nil unpins.
	if err := trackerRepo.SetReadingSource(context.Background(), 1, trackerID, nil); err != nil {
		t.Fatalf("clear pin: %v", err)
	}
	if got := readPin(); got != nil {
		t.Fatalf("pin not cleared: %v", got)
	}

	// Another profile cannot touch it.
	if err := trackerRepo.SetReadingSource(context.Background(), 1, trackerID, &comick); err != nil {
		t.Fatalf("re-pin: %v", err)
	}
	if err := trackerRepo.SetReadingSource(context.Background(), 2, trackerID, nil); err != nil {
		t.Fatalf("clear as other profile: %v", err)
	}
	if got := readPin(); got != comick {
		t.Fatalf("other profile cleared the pin: %v", got)
	}
}
