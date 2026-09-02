package repository_test

import (
	"context"
	"database/sql"
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
