package handlers_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
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
