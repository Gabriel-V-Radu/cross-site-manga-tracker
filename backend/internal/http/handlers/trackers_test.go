package handlers_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
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

	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item has invalid type")
	}
	title, _ := item["title"].(string)
	if title != "API Behind Tracker" {
		t.Fatalf("expected API Behind Tracker, got %q", title)
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
