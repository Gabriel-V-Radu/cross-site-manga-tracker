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
)

func seedGuardTracker(t *testing.T, db *sql.DB, profileID int64, title string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`, profileID, title, 1, "https://mangadex.org/title/"+strings.ToLower(strings.ReplaceAll(title, " ", "-")), "reading", 12.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tracker id: %v", err)
	}
	return id
}

func postForm(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, path string, form url.Values, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("%s %s: %v", http.MethodPost, path, err)
	}
	return res
}

func countTrackers(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM trackers`).Scan(&count); err != nil {
		t.Fatalf("count trackers: %v", err)
	}
	return count
}

// A form value the server refuses must refuse the whole save. The tag ids used
// to be parsed after the INSERT: the response was a 400 and the tracker existed
// anyway, so the reader's retry created it twice.
func TestCreateFromFormRefusesBeforeWritingOnInvalidTags(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	form := url.Values{}
	form.Set("title", "Half Saved")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/half-saved")
	form.Set("status", "reading")
	form.Add("tag_ids", "not-a-number")

	res := postForm(t, app, "/dashboard/trackers", form, nil)
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 400, got %d (body: %s)", res.StatusCode, body)
	}
	if got := countTrackers(t, db); got != 0 {
		t.Fatalf("a refused create must write nothing, found %d trackers", got)
	}
}

// Same rule on edit, for the pasted-URL field: a URL no connector claims used
// to answer 400 after the title and the linked sources had already been
// rewritten. With an empty registry every host is unclaimed.
func TestUpdateFromFormRefusesBeforeWritingOnUnlinkableURL(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedGuardTracker(t, db, 1, "Original Title")

	form := url.Values{}
	form.Set("title", "Rewritten Title")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/original-title")
	form.Set("status", "reading")
	form.Set("linked_url", "https://nobody-claims-this.example/series/x")

	res := postForm(t, app, "/dashboard/trackers/"+strconv.FormatInt(id, 10), form, nil)
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 400, got %d (body: %s)", res.StatusCode, body)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Could not link the pasted URL") {
		t.Fatalf("the refusal must name the field, got %q", body)
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM trackers WHERE id = ?`, id).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "Original Title" {
		t.Fatalf("a refused update must write nothing, title is now %q", title)
	}
}

// The form path enforces the same closed status set as the JSON API. A junk
// status used to reach the database's CHECK constraint and come back as a 500.
func TestCreateFromFormRejectsUnknownStatus(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	form := url.Values{}
	form.Set("title", "Odd Status")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/odd-status")
	form.Set("status", "binge")

	res := postForm(t, app, "/dashboard/trackers", form, nil)
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 400, got %d (body: %s)", res.StatusCode, body)
	}
	if got := countTrackers(t, db); got != 0 {
		t.Fatalf("expected no tracker, found %d", got)
	}
}

func TestCreateFromFormRejectsNonHTTPSourceURL(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	form := url.Values{}
	form.Set("title", "Odd URL")
	form.Set("source_id", "1")
	form.Set("source_url", "javascript:alert(1)")
	form.Set("status", "reading")

	res := postForm(t, app, "/dashboard/trackers", form, nil)
	if res.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 400, got %d (body: %s)", res.StatusCode, body)
	}
	if got := countTrackers(t, db); got != 0 {
		t.Fatalf("expected no tracker, found %d", got)
	}
}

// A browser that says the POST came from another site is refused before any
// handler runs. The dashboard's forms carry no token, and the profile resolver
// falls back to the default profile when no cookie arrives, so without this a
// form auto-submitted from any other page could delete trackers on the LAN
// instance.
func TestDashboardWritesRefuseCrossSiteRequests(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedGuardTracker(t, db, 1, "Guarded Tracker")
	path := "/dashboard/trackers/" + strconv.FormatInt(id, 10) + "/delete"

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{name: "Sec-Fetch-Site cross-site", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{name: "foreign Origin", headers: map[string]string{"Origin": "http://evil.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postForm(t, app, path, url.Values{}, tc.headers)
			if res.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("expected 403, got %d (body: %s)", res.StatusCode, body)
			}
		})
	}
	if got := countTrackers(t, db); got != 1 {
		t.Fatalf("the refused deletes must not have run, found %d trackers", got)
	}

	// A same-origin request goes through: httptest requests arrive on
	// example.com, so that is the origin that matches.
	res := postForm(t, app, path, url.Values{}, map[string]string{
		"Origin":         "http://example.com",
		"Sec-Fetch-Site": "same-origin",
	})
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("same-origin delete: expected 200, got %d (body: %s)", res.StatusCode, body)
	}
	if got := countTrackers(t, db); got != 0 {
		t.Fatalf("the same-origin delete must have run, found %d trackers", got)
	}
}

// Reads are never refused: a bookmarked dashboard URL opened from anywhere is a
// GET.
func TestDashboardReadsIgnoreOrigin(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("GET /dashboard/trackers: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
}

// Every htmx request from a page names that page's profile in a header (set on
// <body>), so an action on a card belongs to the profile the page was rendered
// for even when another tab has since moved the cookie to the other profile.
func TestProfileHeaderWinsOverCookie(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedGuardTracker(t, db, 1, "Profile One Tracker")
	path := "/dashboard/trackers/" + strconv.FormatInt(id, 10) + "/set-last-read"

	// The cookie says profile 2 (another tab switched); the page says profile1.
	res := postForm(t, app, path, url.Values{}, map[string]string{
		"Cookie":        "active_profile_id=2",
		"X-Profile-Key": "profile1",
	})
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected the header's profile to find the tracker, got %d (body: %s)", res.StatusCode, body)
	}

	// And the page's own profile is what the header carries.
	req := httptest.NewRequest(http.MethodGet, "/dashboard?profile=profile2", nil)
	pageRes, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	page, _ := io.ReadAll(pageRes.Body)
	if !strings.Contains(string(page), `hx-headers='{"X-Profile-Key": "profile2"}'`) {
		t.Fatalf("expected the page body to carry the active profile header, got:\n%s", firstLines(string(page), 40))
	}
}

func firstLines(text string, n int) string {
	lines := strings.SplitN(text, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
