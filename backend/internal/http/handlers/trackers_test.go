package handlers_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
	"github.com/gofiber/fiber/v2"
)

func setupTestApp(t *testing.T) (*sql.DB, *fiber.App, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	_, currentFile, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(currentFile)
	backendRoot := filepath.Clean(filepath.Join(baseDir, "..", "..", ".."))
	originalWD, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(backendRoot); err != nil {
		_ = db.Close()
		t.Fatalf("set working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	migrationsPath := filepath.Join(baseDir, "..", "..", "..", "migrations")
	if err := database.ApplyMigrations(db, migrationsPath); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		_ = db.Close()
		t.Fatalf("seed defaults: %v", err)
	}

	cfg := config.Config{AppName: "test-app"}
	app := apihttp.NewServer(cfg, db)

	cleanup := func() {
		_ = app.Shutdown()
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return db, app, cleanup
}

func TestTrackersCRUD(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	createBody := map[string]any{
		"title":              "Blue Lock",
		"sourceId":           1,
		"sourceUrl":          "https://mangadex.org/title/1",
		"status":             "reading",
		"lastReadChapter":    20.0,
		"latestKnownChapter": 22.0,
	}
	body, _ := json.Marshal(createBody)
	createReq := httptest.NewRequest(http.MethodPost, "/v1/trackers", bytes.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRes, err := app.Test(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createRes.StatusCode)
	}

	var created map[string]any
	if err := json.NewDecoder(createRes.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	id := int(created["id"].(float64))

	listReq := httptest.NewRequest(http.MethodGet, "/v1/trackers?status=reading&sort=title&order=asc", nil)
	listRes, err := app.Test(listReq)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRes.StatusCode)
	}

	var listPayload map[string]any
	if err := json.NewDecoder(listRes.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	items := listPayload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 list item, got %d", len(items))
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/trackers/"+toString(id), nil)
	getRes, err := app.Test(getReq)
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRes.StatusCode)
	}

	updateBody := map[string]any{
		"title":              "Blue Lock Updated",
		"sourceId":           1,
		"sourceUrl":          "https://mangadex.org/title/1",
		"status":             "completed",
		"lastReadChapter":    30.0,
		"latestKnownChapter": 30.0,
	}
	updateRaw, _ := json.Marshal(updateBody)
	updateReq := httptest.NewRequest(http.MethodPut, "/v1/trackers/"+toString(id), bytes.NewReader(updateRaw))
	updateReq.Header.Set("Content-Type", "application/json")
	updateRes, err := app.Test(updateReq)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	if updateRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", updateRes.StatusCode)
	}

	var updated map[string]any
	if err := json.NewDecoder(updateRes.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated["status"] != "completed" {
		t.Fatalf("expected status completed, got %v", updated["status"])
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/trackers/"+toString(id), nil)
	deleteRes, err := app.Test(deleteReq)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	if deleteRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", deleteRes.StatusCode)
	}

	getAfterDeleteReq := httptest.NewRequest(http.MethodGet, "/v1/trackers/"+toString(id), nil)
	getAfterDeleteRes, err := app.Test(getAfterDeleteReq)
	if err != nil {
		t.Fatalf("get-after-delete request failed: %v", err)
	}
	if getAfterDeleteRes.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", getAfterDeleteRes.StatusCode)
	}
}

func toString(value int) string {
	return strconv.Itoa(value)
}

func TestDashboardReadingFilterExcludesCaughtUpTrackers(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	_, err := db.Exec(`
		INSERT INTO trackers (title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?)
	`,
		"Behind Tracker", 1, "https://mangadex.org/title/behind", "reading", 8.0, 10.0,
		"Caught Up Tracker", 1, "https://mangadex.org/title/caught-up", "reading", 10.0, 10.0,
	)
	if err != nil {
		t.Fatalf("seed trackers: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?status=reading", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("dashboard trackers request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "Behind Tracker") {
		t.Fatalf("expected reading filter response to include unfinished tracker")
	}
	if strings.Contains(html, "Caught Up Tracker") {
		t.Fatalf("expected reading filter response to exclude caught-up tracker")
	}
}

func TestDashboardPaginationRendersNumberButtons(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO trackers (title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare seed stmt: %v", err)
	}

	for index := 1; index <= 130; index++ {
		title := fmt.Sprintf("Pagination Tracker %03d", index)
		sourceURL := fmt.Sprintf("https://mangadex.org/title/pagination-%d", index)
		chapter := float64(index)
		if _, err := stmt.Exec(title, 1, sourceURL, "reading", chapter, chapter+1); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("seed tracker %d: %v", index, err)
		}
	}

	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close seed stmt: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?page=3&status=reading", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("dashboard trackers request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	html := string(body)

	for page := 1; page <= 6; page++ {
		needle := "data-page-value=\"" + strconv.Itoa(page) + "\""
		if !strings.Contains(html, needle) {
			t.Fatalf("expected pagination to contain button for page %d", page)
		}
	}

	if strings.Contains(html, "data-page-value=\"7\"") {
		t.Fatalf("did not expect pagination to contain page 7 button")
	}

	if !strings.Contains(html, "pagination-btn--active") {
		t.Fatalf("expected an active pagination button")
	}

	if !strings.Contains(html, "data-page-value=\"3\"") {
		t.Fatalf("expected current page button to be active")
	}
}

func TestDashboardPaginationCondensesWithEllipses(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO trackers (title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare seed stmt: %v", err)
	}

	for index := 1; index <= 290; index++ {
		title := fmt.Sprintf("Condensed Pagination Tracker %03d", index)
		sourceURL := fmt.Sprintf("https://mangadex.org/title/condensed-%d", index)
		chapter := float64(index)
		if _, err := stmt.Exec(title, 1, sourceURL, "reading", chapter, chapter+1); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("seed tracker %d: %v", index, err)
		}
	}

	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close seed stmt: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?page=6&status=reading", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("dashboard trackers request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	html := string(body)

	for _, page := range []int{1, 4, 5, 6, 7, 8, 13} {
		needle := "data-page-value=\"" + strconv.Itoa(page) + "\""
		if !strings.Contains(html, needle) {
			t.Fatalf("expected condensed pagination to contain page %d", page)
		}
	}

	if strings.Contains(html, "data-page-value=\"2\"") {
		t.Fatalf("did not expect condensed pagination to contain page 2 button")
	}

	if !strings.Contains(html, "pagination-ellipsis") {
		t.Fatalf("expected condensed pagination to include ellipsis")
	}
}

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
}

func TestAPIReadingFilterExcludesCaughtUpTrackers(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	_, err := db.Exec(`
		INSERT INTO trackers (title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?)
	`,
		"API Behind Tracker", 1, "https://mangadex.org/title/api-behind", "reading", 12.0, 15.0,
		"API Caught Up Tracker", 1, "https://mangadex.org/title/api-caught-up", "reading", 15.0, 15.0,
	)
	if err != nil {
		t.Fatalf("seed trackers: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/trackers?status=reading", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("api trackers request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	var payload map[string]any
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode api list response: %v", err)
	}

	items, ok := payload["items"].([]any)
	if !ok {
		t.Fatalf("items missing or invalid type")
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item in reading filter, got %d", len(items))
	}

	titles := map[string]bool{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("item has invalid type")
		}
		title, _ := item["title"].(string)
		titles[title] = true
	}

	if !titles["API Behind Tracker"] {
		t.Fatalf("expected API Behind Tracker in reading filter response")
	}
	if titles["API Caught Up Tracker"] {
		t.Fatalf("expected API Caught Up Tracker to be excluded from reading filter response")
	}
}

func TestTrackersAreIsolatedByProfile(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	createBody := map[string]any{
		"title":              "Only Profile 1",
		"sourceId":           1,
		"sourceUrl":          "https://mangadex.org/title/isolated",
		"status":             "reading",
		"lastReadChapter":    1.0,
		"latestKnownChapter": 2.0,
	}
	body, _ := json.Marshal(createBody)
	createReq := httptest.NewRequest(http.MethodPost, "/v1/trackers?profile=profile1", bytes.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRes, err := app.Test(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createRes.StatusCode)
	}

	listProfile2Req := httptest.NewRequest(http.MethodGet, "/v1/trackers?profile=profile2", nil)
	listProfile2Res, err := app.Test(listProfile2Req)
	if err != nil {
		t.Fatalf("list profile2 request failed: %v", err)
	}
	if listProfile2Res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listProfile2Res.StatusCode)
	}

	var profile2Payload map[string]any
	if err := json.NewDecoder(listProfile2Res.Body).Decode(&profile2Payload); err != nil {
		t.Fatalf("decode profile2 list response: %v", err)
	}
	profile2Items := profile2Payload["items"].([]any)
	if len(profile2Items) != 0 {
		t.Fatalf("expected 0 items in profile2, got %d", len(profile2Items))
	}

	listProfile1Req := httptest.NewRequest(http.MethodGet, "/v1/trackers?profile=profile1", nil)
	listProfile1Res, err := app.Test(listProfile1Req)
	if err != nil {
		t.Fatalf("list profile1 request failed: %v", err)
	}
	if listProfile1Res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listProfile1Res.StatusCode)
	}

	var profile1Payload map[string]any
	if err := json.NewDecoder(listProfile1Res.Body).Decode(&profile1Payload); err != nil {
		t.Fatalf("decode profile1 list response: %v", err)
	}
	profile1Items := profile1Payload["items"].([]any)
	if len(profile1Items) != 1 {
		t.Fatalf("expected 1 item in profile1, got %d", len(profile1Items))
	}
}

func TestProfileSwitchFromMenuUsesPostedProfile(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	form := url.Values{}
	form.Set("profile", "profile2")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/switch?profile=profile1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("switch profile request failed: %v", err)
	}
	if res.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 303, got %d (body: %s)", res.StatusCode, string(body))
	}

	location := res.Header.Get("Location")
	if location != "/dashboard?profile=profile2" {
		t.Fatalf("expected redirect to profile2, got %q", location)
	}
}

func TestEditTrackerDeletingOriginalLinkedSourcePromotesRemainingSource(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, 1, "Linked Source Switch", 1, "https://mangadex.org/title/original", "reading", 5.0, 10.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	trackerID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tracker id: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
		VALUES (?, ?, ?, ?), (?, ?, ?, ?)
	`,
		trackerID, 1, "original", "https://mangadex.org/title/original",
		trackerID, 2, "replacement", "https://mangaplus.shueisha.co.jp/titles/100",
	)
	if err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	linkedJSON := `[{"sourceId":2,"sourceItemId":"replacement","sourceUrl":"https://mangaplus.shueisha.co.jp/titles/100"}]`
	form := url.Values{}
	form.Set("title", "Linked Source Switch")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/original")
	form.Set("status", "reading")
	form.Set("last_read_chapter", "5")
	form.Set("latest_known_chapter", "10")
	form.Set("linked_sources_json", linkedJSON)

	req := httptest.NewRequest(http.MethodPost, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("dashboard update request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	row := db.QueryRow(`SELECT source_id, source_url FROM trackers WHERE id = ?`, trackerID)
	var sourceID int64
	var sourceURL string
	if err := row.Scan(&sourceID, &sourceURL); err != nil {
		t.Fatalf("load updated tracker: %v", err)
	}

	if sourceID != 2 {
		t.Fatalf("expected tracker source_id to switch to linked source 2, got %d", sourceID)
	}
	if sourceURL != "https://mangaplus.shueisha.co.jp/titles/100" {
		t.Fatalf("expected tracker source_url to switch to linked source URL, got %s", sourceURL)
	}

	rows, err := db.Query(`SELECT source_id, source_url FROM tracker_sources WHERE tracker_id = ? ORDER BY source_id ASC`, trackerID)
	if err != nil {
		t.Fatalf("query tracker_sources: %v", err)
	}
	defer rows.Close()

	type trackerSourceRow struct {
		sourceID  int64
		sourceURL string
	}
	items := make([]trackerSourceRow, 0)
	for rows.Next() {
		var item trackerSourceRow
		if err := rows.Scan(&item.sourceID, &item.sourceURL); err != nil {
			t.Fatalf("scan tracker source row: %v", err)
		}
		items = append(items, item)
	}

	if len(items) != 1 {
		t.Fatalf("expected exactly 1 linked source after deletion, got %d", len(items))
	}
	if items[0].sourceID != 2 {
		t.Fatalf("expected remaining linked source id 2, got %d", items[0].sourceID)
	}
}

func TestDashboardTagFilterUsesAndSemanticsAndRendersIcons(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?)
	`,
		1, "Tagged Match", 1, "https://mangadex.org/title/tagged-match", "reading", 3.0, 10.0,
		1, "Tagged Partial", 1, "https://mangadex.org/title/tagged-partial", "reading", 1.0, 8.0,
	)
	if err != nil {
		t.Fatalf("seed trackers: %v", err)
	}

	_, err = result.LastInsertId()
	if err != nil {
		t.Fatalf("last tracker id: %v", err)
	}

	var firstTrackerID int64
	if err := db.QueryRow(`SELECT id FROM trackers WHERE profile_id = ? AND title = ?`, 1, "Tagged Match").Scan(&firstTrackerID); err != nil {
		t.Fatalf("load first tracker id: %v", err)
	}

	var secondTrackerID int64
	if err := db.QueryRow(`SELECT id FROM trackers WHERE profile_id = ? AND title = ?`, 1, "Tagged Partial").Scan(&secondTrackerID); err != nil {
		t.Fatalf("load second tracker id: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO custom_tags (profile_id, name, icon_key)
		VALUES (?, ?, ?), (?, ?, ?)
	`,
		1, "favorite", "icon_1",
		1, "priority", nil,
	)
	if err != nil {
		t.Fatalf("seed custom tags: %v", err)
	}

	var favoriteTagID int64
	if err := db.QueryRow(`SELECT id FROM custom_tags WHERE profile_id = ? AND name = ?`, 1, "favorite").Scan(&favoriteTagID); err != nil {
		t.Fatalf("load favorite tag id: %v", err)
	}

	var priorityTagID int64
	if err := db.QueryRow(`SELECT id FROM custom_tags WHERE profile_id = ? AND name = ?`, 1, "priority").Scan(&priorityTagID); err != nil {
		t.Fatalf("load priority tag id: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO tracker_tags (tracker_id, tag_id)
		VALUES (?, ?), (?, ?), (?, ?)
	`,
		firstTrackerID, favoriteTagID,
		firstTrackerID, priorityTagID,
		secondTrackerID, favoriteTagID,
	)
	if err != nil {
		t.Fatalf("seed tracker tags: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?status=reading&tags=favorite,priority", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("dashboard trackers request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "Tagged Match") {
		t.Fatalf("expected AND tag filter response to include fully tagged tracker")
	}
	if strings.Contains(html, "Tagged Partial") {
		t.Fatalf("expected AND tag filter response to exclude partially tagged tracker")
	}
	if !strings.Contains(html, "/assets/tag-icons/icon-star-gold.svg") {
		t.Fatalf("expected tracker card to render tag icon overlay")
	}

	reqMulti := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?status=reading&tags=favorite&tags=priority", nil)
	resMulti, err := app.Test(reqMulti)
	if err != nil {
		t.Fatalf("dashboard trackers multi-tag request failed: %v", err)
	}
	if resMulti.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resMulti.Body)
		t.Fatalf("expected 200 for multi-tag query, got %d (body: %s)", resMulti.StatusCode, string(body))
	}

	bodyMulti, err := io.ReadAll(resMulti.Body)
	if err != nil {
		t.Fatalf("read multi-tag response body: %v", err)
	}
	htmlMulti := string(bodyMulti)

	if !strings.Contains(htmlMulti, "Tagged Match") {
		t.Fatalf("expected multi-tag query to include fully tagged tracker")
	}
	if strings.Contains(htmlMulti, "Tagged Partial") {
		t.Fatalf("expected multi-tag query to exclude partially tagged tracker")
	}
}

func TestDashboardLinkedSitesFilterSupportsMultipleSelections(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	_, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?)
	`,
		1, "Linked Sites Match", 1, "https://mangadex.org/title/linked-sites-match", "reading", 2.0, 8.0,
		1, "Linked Sites Partial", 1, "https://mangadex.org/title/linked-sites-partial", "reading", 3.0, 7.0,
		1, "Linked Sites Other", 2, "https://mangaplus.shueisha.co.jp/titles/other", "reading", 1.0, 9.0,
	)
	if err != nil {
		t.Fatalf("seed trackers: %v", err)
	}

	var matchTrackerID int64
	if err := db.QueryRow(`SELECT id FROM trackers WHERE profile_id = ? AND title = ?`, 1, "Linked Sites Match").Scan(&matchTrackerID); err != nil {
		t.Fatalf("load match tracker id: %v", err)
	}

	var partialTrackerID int64
	if err := db.QueryRow(`SELECT id FROM trackers WHERE profile_id = ? AND title = ?`, 1, "Linked Sites Partial").Scan(&partialTrackerID); err != nil {
		t.Fatalf("load partial tracker id: %v", err)
	}

	var otherTrackerID int64
	if err := db.QueryRow(`SELECT id FROM trackers WHERE profile_id = ? AND title = ?`, 1, "Linked Sites Other").Scan(&otherTrackerID); err != nil {
		t.Fatalf("load other tracker id: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
		VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)
	`,
		matchTrackerID, 1, "match-1", "https://mangadex.org/title/linked-sites-match",
		matchTrackerID, 2, "match-2", "https://mangaplus.shueisha.co.jp/titles/match",
		partialTrackerID, 1, "partial-1", "https://mangadex.org/title/linked-sites-partial",
		otherTrackerID, 2, "other-2", "https://mangaplus.shueisha.co.jp/titles/other",
	)
	if err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?status=reading&sites=1&sites=2", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("dashboard trackers linked-sites request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read linked-sites response body: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "Linked Sites Match") {
		t.Fatalf("expected multi-site OR filter to include tracker linked to selected sites")
	}
	if !strings.Contains(html, "Linked Sites Partial") {
		t.Fatalf("expected multi-site OR filter to include tracker linked to site 1")
	}
	if !strings.Contains(html, "Linked Sites Other") {
		t.Fatalf("expected multi-site OR filter to include tracker linked to site 2")
	}
}

func TestProfileLinkedSitesPartialPreservesSelectedValues(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, 1, "Linked Sites Partial Endpoint", 1, "https://mangadex.org/title/linked-sites-partial-endpoint", "reading", 1.0, 2.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	trackerID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tracker id: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
		VALUES (?, ?, ?, ?), (?, ?, ?, ?)
	`,
		trackerID, 1, "partial-endpoint-1", "https://mangadex.org/title/linked-sites-partial-endpoint",
		trackerID, 2, "partial-endpoint-2", "https://mangaplus.shueisha.co.jp/titles/partial-endpoint",
	)
	if err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/profile/filter-linked-sites?profile=profile1&sites=2", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("linked sites partial request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read linked sites partial body: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "name=\"sites\" value=\"2\" checked") {
		t.Fatalf("expected linked sites partial to preserve selected site 2")
	}
	if strings.Contains(html, "name=\"sites\" value=\"1\" checked") {
		t.Fatalf("expected linked sites partial to keep site 1 unselected")
	}
}

func TestDeleteTagFromMenuRefreshesFilterTagOptions(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO custom_tags (profile_id, name)
		VALUES (?, ?)
	`, 1, "to-remove")
	if err != nil {
		t.Fatalf("seed custom tag: %v", err)
	}

	tagID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("custom tag id: %v", err)
	}

	deleteForm := url.Values{}
	deleteForm.Set("tag_id", strconv.FormatInt(tagID, 10))
	deleteReq := httptest.NewRequest(http.MethodPost, "/dashboard/profile/tags/delete?profile=profile1", strings.NewReader(deleteForm.Encode()))
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	deleteRes, err := app.Test(deleteReq)
	if err != nil {
		t.Fatalf("delete tag request failed: %v", err)
	}
	if deleteRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(deleteRes.Body)
		t.Fatalf("expected 200, got %d (body: %s)", deleteRes.StatusCode, string(body))
	}

	hxTrigger := deleteRes.Header.Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "\"profileTagsChanged\":true") {
		t.Fatalf("expected HX-Trigger to include profileTagsChanged, got %q", hxTrigger)
	}

	partialReq := httptest.NewRequest(http.MethodGet, "/dashboard/profile/filter-tags?profile=profile1", nil)
	partialRes, err := app.Test(partialReq)
	if err != nil {
		t.Fatalf("filter tags partial request failed: %v", err)
	}
	if partialRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(partialRes.Body)
		t.Fatalf("expected 200, got %d (body: %s)", partialRes.StatusCode, string(body))
	}

	partialBody, err := io.ReadAll(partialRes.Body)
	if err != nil {
		t.Fatalf("read filter tags partial body: %v", err)
	}
	html := string(partialBody)

	if strings.Contains(html, "to-remove") {
		t.Fatalf("expected deleted tag to be removed from filter options")
	}
	if !strings.Contains(html, "No tags created yet.") {
		t.Fatalf("expected empty state after deleting only tag")
	}
}

func TestRenameTagFromMenuRefreshesFilterTagOptions(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO custom_tags (profile_id, name)
		VALUES (?, ?)
	`, 1, "old-name")
	if err != nil {
		t.Fatalf("seed custom tag: %v", err)
	}

	tagID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("custom tag id: %v", err)
	}

	renameForm := url.Values{}
	renameForm.Set("tag_id", strconv.FormatInt(tagID, 10))
	renameForm.Set("tag_name", "new-name")
	renameReq := httptest.NewRequest(http.MethodPost, "/dashboard/profile/tags/rename?profile=profile1", strings.NewReader(renameForm.Encode()))
	renameReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	renameRes, err := app.Test(renameReq)
	if err != nil {
		t.Fatalf("rename tag request failed: %v", err)
	}
	if renameRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(renameRes.Body)
		t.Fatalf("expected 200, got %d (body: %s)", renameRes.StatusCode, string(body))
	}

	hxTrigger := renameRes.Header.Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "\"profileTagsChanged\":true") {
		t.Fatalf("expected HX-Trigger to include profileTagsChanged, got %q", hxTrigger)
	}

	renameBody, err := io.ReadAll(renameRes.Body)
	if err != nil {
		t.Fatalf("read rename response body: %v", err)
	}
	renameHTML := string(renameBody)
	if !strings.Contains(renameHTML, "Tag renamed") {
		t.Fatalf("expected rename success feedback message")
	}

	partialReq := httptest.NewRequest(http.MethodGet, "/dashboard/profile/filter-tags?profile=profile1", nil)
	partialRes, err := app.Test(partialReq)
	if err != nil {
		t.Fatalf("filter tags partial request failed: %v", err)
	}
	if partialRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(partialRes.Body)
		t.Fatalf("expected 200, got %d (body: %s)", partialRes.StatusCode, string(body))
	}

	partialBody, err := io.ReadAll(partialRes.Body)
	if err != nil {
		t.Fatalf("read filter tags partial body: %v", err)
	}
	html := string(partialBody)

	if strings.Contains(html, "old-name") {
		t.Fatalf("expected old tag name to be removed from filter options")
	}
	if !strings.Contains(html, "new-name") {
		t.Fatalf("expected renamed tag to appear in filter options")
	}
}
