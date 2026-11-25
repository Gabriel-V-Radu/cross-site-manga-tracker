package handlers_test

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// A stored link another site contradicts is shown in its own section with
// what it would replace, and Replace swaps the link.
func TestLinksQueueOffersAndAppliesAReplacement(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	mangaFire := linksSourceID(t, db, "mangafire")
	mangaUpdates := linksSourceID(t, db, "mangaupdates")
	const (
		novel = "https://www.mangaupdates.com/series/q5n1lxg/slime-novel"
		manga = "https://www.mangaupdates.com/series/04gtb06/slime"
	)
	trackerID := linksSeedTracker(t, db, 1, "That Time I Got Reincarnated as a Slime", mangaFire, "https://mangafire.to/title/abc12-slime")
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?)`, trackerID, mangaUpdates, novel); err != nil {
		t.Fatalf("link the novel: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO source_link_suggestions
			(tracker_id, source_id, candidate_url, candidate_title, candidate_latest_chapter, score, status, basis, replaces_url)
		VALUES (?, ?, ?, 'Tensei Shitara Slime datta Ken', 106, 1.0, 'pending', 'mangafire', ?)
	`, trackerID, mangaUpdates, manga, novel)
	if err != nil {
		t.Fatalf("seed replacement: %v", err)
	}
	suggestionID, _ := result.LastInsertId()

	status, body := linksRequest(t, app, "GET", "/dashboard/links/queue?source="+toString(int(mangaUpdates)))
	if status != 200 {
		t.Fatalf("queue status = %d: %s", status, body)
	}
	for _, want := range []string{"Stored links that disagree", "Linked now:", novel, "Tensei Shitara Slime datta Ken", "Same series per MangaFire", "Replace", "Keep current", "1</strong> stored link(s) to check"} {
		if !strings.Contains(body, want) {
			t.Fatalf("queue misses %q: %s", want, body)
		}
	}

	res, err := app.Test(httptest.NewRequest("POST", "/dashboard/links/suggestions/"+toString(int(suggestionID))+"/accept", nil), -1)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	acceptBody, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || strings.Contains(string(acceptBody), "link-card-"+toString(int(trackerID))) {
		t.Fatalf("accept = %d, card still rendered: %s", res.StatusCode, acceptBody)
	}

	var url string
	if err := db.QueryRow(`SELECT source_url FROM tracker_sources WHERE tracker_id = ? AND source_id = ?`, trackerID, mangaUpdates).Scan(&url); err != nil {
		t.Fatalf("read link: %v", err)
	}
	if url != manga {
		t.Fatalf("link = %q, want the manga", url)
	}
}

// An id match on a site that is behind is labelled so and kept out of the
// bulk accept.
func TestLinksQueueLabelsAnIDMatchThatIsBehind(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	mangaFire := linksSourceID(t, db, "mangafire")
	mangaDex := linksSourceID(t, db, "mangadex")
	trackerID := linksSeedTracker(t, db, 1, "Evolution Begins With a Big Tree", mangaFire, "https://mangafire.to/title/abc12-tree")
	if _, err := db.Exec(`UPDATE trackers SET latest_known_chapter = 542 WHERE id = ?`, trackerID); err != nil {
		t.Fatalf("set chapter: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO source_link_suggestions
			(tracker_id, source_id, candidate_url, candidate_title, candidate_latest_chapter, score, status, basis)
		VALUES (?, ?, 'https://mangadex.org/title/2707b1cc-05b9-455f-ae6f-f351dd9f8741', 'Evolution Begins With a Big Tree', 114, 0.9, 'pending', 'mangafire')
	`, trackerID, mangaDex); err != nil {
		t.Fatalf("seed suggestion: %v", err)
	}

	status, body := linksRequest(t, app, "GET", "/dashboard/links/queue?source="+toString(int(mangaDex)))
	if status != 200 {
		t.Fatalf("queue status = %d: %s", status, body)
	}
	if !strings.Contains(body, "Same series per MangaFire, but behind") || !strings.Contains(body, "behind your chapter") {
		t.Fatalf("queue does not say the match is behind: %s", body)
	}
	if strings.Contains(body, "exact match(es)") {
		t.Fatalf("a match that is behind was offered for the bulk accept: %s", body)
	}
}
