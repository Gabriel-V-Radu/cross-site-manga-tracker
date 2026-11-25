package repository_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// A replacement is a candidate for a site the tracker is already linked to:
// another site's cross link says the series lives elsewhere there (the manga's
// MangaUpdates entry where the novel's is linked). These tests pin how one is
// stored, listed and decided.

const (
	novelURL = "https://www.mangaupdates.com/series/q5n1lxg/slime-novel"
	mangaURL = "https://www.mangaupdates.com/series/04gtb06/slime"
)

func seedLinkRow(t *testing.T, db *sql.DB, trackerID int64, sourceID int64, url string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url, in_step, last_polled_at)
		VALUES (?, ?, 'old-item', ?, 1, CURRENT_TIMESTAMP)
	`, trackerID, sourceID, url); err != nil {
		t.Fatalf("link row: %v", err)
	}
}

func replacementFor(trackerID int64, sourceID int64) *repository.LinkSuggestion {
	replaces := novelURL
	itemID := "4gtb06"
	return &repository.LinkSuggestion{
		TrackerID:       trackerID,
		SourceID:        sourceID,
		CandidateURL:    mangaURL,
		CandidateItemID: &itemID,
		CandidateTitle:  "Tensei Shitara Slime datta Ken",
		Score:           1.0,
		Basis:           "mangafire",
		ReplacesURL:     &replaces,
	}
}

func TestLinkedScanTargetsCarryTheirLinks(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	mangaFire := sourceIDByKey(t, db, "mangafire")
	mangaUpdates := sourceIDByKey(t, db, "mangaupdates")

	linked := seedLinkTracker(t, db, "linked")
	seedLinkRow(t, db, linked, mangaFire, "https://mangafire.to/manga/linked")
	seedLinkRow(t, db, linked, mangaUpdates, novelURL)
	unlinked := seedLinkTracker(t, db, "unlinked")
	seedLinkRow(t, db, unlinked, mangaFire, "https://mangafire.to/manga/unlinked")

	targets, err := repo.ListLinkedScanTargets(context.Background(), 1, mangaUpdates, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list linked: %v", err)
	}
	if len(targets) != 1 || targets[0].TrackerID != linked {
		t.Fatalf("linked targets = %+v, want only the linked tracker", targets)
	}
	if len(targets[0].CurrentLinks) != 1 || targets[0].CurrentLinks[0] != novelURL || len(targets[0].Links) != 2 {
		t.Fatalf("target links = %+v / %v", targets[0].Links, targets[0].CurrentLinks)
	}

	// The ordinary targets carry their links too: the scan asks them where
	// else the series lives.
	unlinkedTargets, err := repo.ListScanTargets(context.Background(), 1, mangaUpdates, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(unlinkedTargets) != 1 || len(unlinkedTargets[0].Links) != 1 || unlinkedTargets[0].Links[0].SourceKey != "mangafire" {
		t.Fatalf("targets = %+v, want the unlinked tracker with its MangaFire link", unlinkedTargets)
	}
}

func TestAcceptingAReplacementSwapsTheLink(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	ctx := context.Background()
	mangaUpdates := sourceIDByKey(t, db, "mangaupdates")
	trackerID := seedLinkTracker(t, db, "slime")
	seedLinkRow(t, db, trackerID, mangaUpdates, novelURL)
	// The review that linked the novel left an accepted row for it.
	if _, err := db.Exec(`
		INSERT INTO source_link_suggestions (tracker_id, source_id, candidate_url, candidate_title, score, status)
		VALUES (?, ?, ?, 'Slime (Novel)', 1, 'accepted')
	`, trackerID, mangaUpdates, novelURL); err != nil {
		t.Fatalf("seed accepted row: %v", err)
	}

	if err := repo.ReplacePendingReplacement(ctx, trackerID, mangaUpdates, replacementFor(trackerID, mangaUpdates)); err != nil {
		t.Fatalf("store replacement: %v", err)
	}

	// It is listed as a replacement and stays out of the ordinary queue.
	replacements, err := repo.ListReplacements(ctx, 1, mangaUpdates)
	if err != nil {
		t.Fatalf("list replacements: %v", err)
	}
	if len(replacements) != 1 || len(replacements[0].Suggestions) != 1 {
		t.Fatalf("replacements = %+v, want one", replacements)
	}
	offer := replacements[0].Suggestions[0]
	if offer.ReplacesURL == nil || *offer.ReplacesURL != novelURL || offer.Basis != "mangafire" || replacements[0].Title != "slime" {
		t.Fatalf("offer = %+v", offer)
	}
	queue, err := repo.ListReviewQueue(ctx, 1, mangaUpdates, repository.LinkScanFilter{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue = %+v; a linked tracker is not in the ordinary queue", queue)
	}

	accepted, err := repo.AcceptSuggestion(ctx, 1, offer.ID)
	if err != nil || accepted == nil {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}

	var url, itemID string
	var inStep int
	var lastPolled sql.NullTime
	if err := db.QueryRow(`
		SELECT source_url, source_item_id, in_step, last_polled_at FROM tracker_sources
		WHERE tracker_id = ? AND source_id = ?
	`, trackerID, mangaUpdates).Scan(&url, &itemID, &inStep, &lastPolled); err != nil {
		t.Fatalf("read link: %v", err)
	}
	if url != mangaURL || itemID != "4gtb06" || inStep != 0 || lastPolled.Valid {
		t.Fatalf("link = %s %s in_step=%d polled=%v; want the manga, with what the poller knew of the novel cleared", url, itemID, inStep, lastPolled)
	}
	if got := countLinkRows(t, db, trackerID, mangaUpdates); got != 1 {
		t.Fatalf("link rows = %d, want the one row replaced in place", got)
	}
	if got := suggestionStatusByURL(t, db, novelURL); got != repository.LinkSuggestionRejected {
		t.Fatalf("the replaced novel is %q, want rejected so no scan offers it back", got)
	}
	if got := suggestionStatusByURL(t, db, mangaURL); got != repository.LinkSuggestionAccepted {
		t.Fatalf("the replacement is %q, want accepted", got)
	}
	decided, err := repo.DecidedCandidateURLs(ctx, trackerID, mangaUpdates)
	if err != nil || len(decided) != 2 {
		t.Fatalf("decided = %v, %v; want both URLs", decided, err)
	}
}

func TestRejectingAReplacementKeepsTheLinkForGood(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	ctx := context.Background()
	mangaUpdates := sourceIDByKey(t, db, "mangaupdates")
	trackerID := seedLinkTracker(t, db, "slime")
	seedLinkRow(t, db, trackerID, mangaUpdates, novelURL)

	if err := repo.ReplacePendingReplacement(ctx, trackerID, mangaUpdates, replacementFor(trackerID, mangaUpdates)); err != nil {
		t.Fatalf("store replacement: %v", err)
	}
	if err := repo.DecideSuggestion(ctx, 1, suggestionIDByURL(t, db, mangaURL), repository.LinkSuggestionRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// The next scan finds the same disagreement; the decision stands.
	if err := repo.ReplacePendingReplacement(ctx, trackerID, mangaUpdates, replacementFor(trackerID, mangaUpdates)); err != nil {
		t.Fatalf("store again: %v", err)
	}
	replacements, err := repo.ListReplacements(ctx, 1, mangaUpdates)
	if err != nil || len(replacements) != 0 {
		t.Fatalf("replacements = %+v, %v; a kept link is not offered again", replacements, err)
	}
	var url string
	if err := db.QueryRow(`SELECT source_url FROM tracker_sources WHERE tracker_id = ? AND source_id = ?`, trackerID, mangaUpdates).Scan(&url); err != nil || url != novelURL {
		t.Fatalf("link = %q, %v; want the kept link", url, err)
	}
}

func TestReplacementIsWithdrawnAndHiddenWhenStale(t *testing.T) {
	db := openLinkSuggestionTestDB(t)
	repo := repository.NewLinkSuggestionRepository(db)
	ctx := context.Background()
	mangaUpdates := sourceIDByKey(t, db, "mangaupdates")

	withdrawn := seedLinkTracker(t, db, "withdrawn")
	seedLinkRow(t, db, withdrawn, mangaUpdates, novelURL)
	if err := repo.ReplacePendingReplacement(ctx, withdrawn, mangaUpdates, replacementFor(withdrawn, mangaUpdates)); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := repo.ReplacePendingReplacement(ctx, withdrawn, mangaUpdates, nil); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	unlinked := seedLinkTracker(t, db, "unlinked since")
	seedLinkRow(t, db, unlinked, mangaUpdates, novelURL)
	if err := repo.ReplacePendingReplacement(ctx, unlinked, mangaUpdates, replacementFor(unlinked, mangaUpdates)); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM tracker_sources WHERE tracker_id = ?`, unlinked); err != nil {
		t.Fatalf("unlink: %v", err)
	}

	replacements, err := repo.ListReplacements(ctx, 1, mangaUpdates)
	if err != nil || len(replacements) != 0 {
		t.Fatalf("replacements = %+v, %v; want none", replacements, err)
	}

	// Accepting the leftover of a link removed since simply links the
	// candidate.
	var leftover int64
	if err := db.QueryRow(`SELECT id FROM source_link_suggestions WHERE tracker_id = ? AND status = 'pending'`, unlinked).Scan(&leftover); err != nil {
		t.Fatalf("leftover: %v", err)
	}
	if accepted, err := repo.AcceptSuggestion(ctx, 1, leftover); err != nil || accepted == nil {
		t.Fatalf("accept leftover = %+v, %v", accepted, err)
	}
	if got := countLinkRows(t, db, unlinked, mangaUpdates); got != 1 {
		t.Fatalf("link rows = %d, want the candidate linked", got)
	}
}
