package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func TestNewTrackerModalRenders(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers/new", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("new tracker modal request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read new tracker modal body: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "<form class=\"tracker-form\"") {
		t.Fatalf("expected new tracker modal to include tracker form")
	}
	if !strings.Contains(html, "name=\"view_mode\"") {
		t.Fatalf("expected new tracker modal to include view_mode hidden input")
	}
}

func TestCreateTrackerFromFormPrependsWithoutImmediateRefresh(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	form := url.Values{}
	form.Set("title", "Prepended Tracker")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/prepended-tracker")
	form.Set("status", "reading")
	form.Set("view_mode", "grid")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/trackers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("create tracker form request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	hxTrigger := res.Header.Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "\"trackerCreated\"") {
		t.Fatalf("expected HX-Trigger to include trackerCreated, got %q", hxTrigger)
	}
	if strings.Contains(hxTrigger, "\"trackersChanged\"") {
		t.Fatalf("expected HX-Trigger to not include trackersChanged, got %q", hxTrigger)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read create tracker form response: %v", err)
	}
	html := string(body)

	if strings.TrimSpace(html) != "" {
		t.Fatalf("expected empty modal response body, got %q", html)
	}
}

func TestTrackerCardFragmentRendersCard(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`, 1, "Fragment Card", 1, "https://mangadex.org/title/fragment-card", "reading", 12.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	trackerID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tracker id: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10)+"/card-fragment?view=grid", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("card fragment request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read card fragment response: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "id=\"tracker-card-"+strconv.FormatInt(trackerID, 10)+"\"") {
		t.Fatalf("expected card fragment to include tracker id")
	}
	if !strings.Contains(html, "Fragment Card") {
		t.Fatalf("expected card fragment to include tracker title")
	}
	if !strings.Contains(html, "tracker-card__source-logo") {
		t.Fatalf("expected card fragment to include source logo overlay")
	}
	if !strings.Contains(html, "tracker-rating__toggle") {
		t.Fatalf("expected card fragment to include rating toggle")
	}
	plusPattern := regexp.MustCompile(`tracker-rating__toggle[^>]*>\s*\+\s*</summary>`)
	if !plusPattern.MatchString(html) {
		t.Fatalf("expected unrated card fragment to render plus rating toggle")
	}
}

func TestSetRatingFromCardReplacesCard(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`, 1, "Rated Tracker", 1, "https://mangadex.org/title/rated-tracker", "reading", 12.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	trackerID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tracker id: %v", err)
	}

	form := url.Values{}
	form.Set("rating", "9.5")
	form.Set("view_mode", "grid")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10)+"/rating", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("set rating request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read set rating response: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "hx-swap-oob=\"outerHTML:#tracker-card-"+strconv.FormatInt(trackerID, 10)+"\"") {
		t.Fatalf("expected set rating to return OOB card replacement")
	}
	scorePattern := regexp.MustCompile(`tracker-rating__toggle[^>]*>\s*9\.5\s*</summary>`)
	if !scorePattern.MatchString(html) {
		t.Fatalf("expected rated card response to render updated score")
	}
}

// canonicalComickConnector resolves every URL to one canonical series page, as
// the real connectors do when they rebuild the URL from the series id. It holds
// no state: the card render after the save may call it from background lookups.
type canonicalComickConnector struct{}

const canonicalComickURL = "https://comick.io/comic/canonical-series"

func (canonicalComickConnector) Key() string                       { return "comick" }
func (canonicalComickConnector) Name() string                      { return "ComicK" }
func (canonicalComickConnector) Kind() string                      { return connectors.KindNative }
func (canonicalComickConnector) HealthCheck(context.Context) error { return nil }
func (canonicalComickConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (canonicalComickConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	chapter := 30.0
	return &connectors.MangaResult{SourceItemID: "canonical-series", URL: canonicalComickURL, LatestChapter: &chapter}, nil
}

// Two stored links on one site that canonicalize to the same URL — a raw link
// kept from a save while the site was unreachable, beside the canonical one —
// used to fail the next links-changing save with a 500: the second INSERT hit
// tracker_sources' unique key and rolled back every edit in the save.
func TestUpdateFromFormFoldsLinksThatCanonicalizeToOneURL(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		if err := registry.Register(canonicalComickConnector{}); err != nil {
			t.Fatalf("register comick: %v", err)
		}
	})
	defer cleanup()

	var comickID, mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}

	const rawComickURL = "https://comick.io/comic/canonical-series/chapter-29"
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (1, 'Folded Links', 1, 'https://mangadex.org/title/folded', 'reading', 29)
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, _ := result.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, 1, ?), (?, ?, ?), (?, ?, ?)
	`, id, "https://mangadex.org/title/folded",
		id, comickID, canonicalComickURL,
		id, comickID, rawComickURL); err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	// The stored links as the modal lists them, plus a MangaFire link the user
	// added, which is what makes this save re-resolve every link.
	linked := `[{"sourceId":1,"sourceUrl":"https://mangadex.org/title/folded"},` +
		`{"sourceId":` + strconv.FormatInt(comickID, 10) + `,"sourceUrl":"` + canonicalComickURL + `"},` +
		`{"sourceId":` + strconv.FormatInt(comickID, 10) + `,"sourceUrl":"` + rawComickURL + `"},` +
		`{"sourceId":` + strconv.FormatInt(mangafireID, 10) + `,"sourceUrl":"https://mangafire.to/manga/folded.x1"}]`
	form := url.Values{}
	form.Set("title", "Folded Links Renamed")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/folded")
	form.Set("status", "reading")
	form.Set("latest_known_chapter", "29")
	form.Set("linked_sources_json", linked)

	res := postForm(t, app, "/dashboard/trackers/"+strconv.FormatInt(id, 10), form, nil)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM trackers WHERE id = ?`, id).Scan(&title); err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if title != "Folded Links Renamed" {
		t.Fatalf("title = %q, the save did not apply", title)
	}
	var comickRows int
	var comickURL string
	if err := db.QueryRow(`
		SELECT COUNT(1), MAX(source_url) FROM tracker_sources WHERE tracker_id = ? AND source_id = ?
	`, id, comickID).Scan(&comickRows, &comickURL); err != nil {
		t.Fatalf("count comick links: %v", err)
	}
	if comickRows != 1 || comickURL != canonicalComickURL {
		t.Fatalf("comick links = %d (%s), want the one canonical row", comickRows, comickURL)
	}
}
