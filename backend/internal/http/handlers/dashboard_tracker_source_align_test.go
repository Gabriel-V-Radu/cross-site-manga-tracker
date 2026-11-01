package handlers_test

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// TestEditTrackerRealignsSourceOnItsURL pins that a save never stores a source
// id beside another site's URL. The form's Source select is also the site
// "Search by Title" searches, so switching it to search MangaHub and saving
// without picking a result stored (MangaHub, mangafire.to URL) — and the
// primary mirror then linked that pair too, failing every poll with "url does
// not belong to mangahub". Re-saving such a tracker must repair it.
func TestEditTrackerRealignsSourceOnItsURL(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		for key, host := range map[string]string{"mangafire": "mangafire.to", "mangahub": "mangahub.io"} {
			if err := registry.Register(unreachableConnector{key: key, hosts: []string{host}}); err != nil {
				t.Fatalf("register %s: %v", key, err)
			}
		}
	})
	defer cleanup()

	sourceID := func(key string) int64 {
		var id int64
		if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, key).Scan(&id); err != nil {
			t.Fatalf("look up %s source: %v", key, err)
		}
		return id
	}
	mangafireID, mangahubID := sourceID("mangafire"), sourceID("mangahub")
	const mangafireURL = "https://mangafire.to/title/yqwpx-overgeared-team-argo"
	const mangahubURL = "https://mangahub.io/manga/overgeared-team-argo"

	// The broken state as it sat in the deployed database.
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (1, 'Overgeared', ?, ?, 'reading', 346)
	`, mangahubID, mangafireURL)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ := result.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url)
		VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)
	`, trackerID, mangafireID, mangafireURL,
		trackerID, mangahubID, mangahubURL,
		trackerID, mangahubID, mangafireURL); err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	// Re-saved exactly as the edit modal renders it.
	linked := `[{"sourceId":` + strconv.FormatInt(mangafireID, 10) + `,"sourceUrl":"` + mangafireURL + `"},` +
		`{"sourceId":` + strconv.FormatInt(mangahubID, 10) + `,"sourceUrl":"` + mangahubURL + `"},` +
		`{"sourceId":` + strconv.FormatInt(mangahubID, 10) + `,"sourceUrl":"` + mangafireURL + `"}]`
	form := url.Values{}
	form.Set("title", "Overgeared")
	form.Set("source_id", strconv.FormatInt(mangahubID, 10))
	form.Set("source_url", mangafireURL)
	form.Set("status", "reading")
	form.Set("latest_known_chapter", "346")
	form.Set("linked_sources_json", linked)

	req := httptest.NewRequest(http.MethodPost, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("dashboard update request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	var gotSourceID int64
	var gotURL string
	if err := db.QueryRow(`SELECT source_id, source_url FROM trackers WHERE id = ?`, trackerID).Scan(&gotSourceID, &gotURL); err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if gotSourceID != mangafireID || gotURL != mangafireURL {
		t.Fatalf("primary = (%d, %s), want MangaFire (%d) with its own URL", gotSourceID, gotURL, mangafireID)
	}

	assertNoMismatchedLink(t, db, trackerID, mangahubID, mangafireURL)
	var links int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, trackerID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 2 {
		t.Fatalf("expected the MangaFire and MangaHub links only, got %d rows", links)
	}
}

func assertNoMismatchedLink(t *testing.T, db *sql.DB, trackerID, sourceID int64, sourceURL string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = ? AND source_url = ?
	`, trackerID, sourceID, sourceURL).Scan(&count); err != nil {
		t.Fatalf("query links: %v", err)
	}
	if count != 0 {
		t.Fatalf("source %d is still linked with the other site's URL %s", sourceID, sourceURL)
	}
}
