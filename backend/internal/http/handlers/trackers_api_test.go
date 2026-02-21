package handlers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrackersCRUD(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	createBody := map[string]any{
		"title":              "Blue Lock",
		"relatedTitles":      []string{"Blue Lock: Episode Nagi"},
		"sourceId":           1,
		"sourceUrl":          "https://mangadex.org/title/1",
		"status":             "reading",
		"lastReadChapter":    20.0,
		"rating":             8.5,
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
		"relatedTitles":      []string{"Blue Lock Alt"},
		"sourceId":           1,
		"sourceUrl":          "https://mangadex.org/title/1",
		"status":             "completed",
		"lastReadChapter":    30.0,
		"rating":             9.5,
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
	if updated["rating"] != 9.5 {
		t.Fatalf("expected rating 9.5, got %v", updated["rating"])
	}
	updatedRelatedTitles, ok := updated["relatedTitles"].([]any)
	if !ok || len(updatedRelatedTitles) != 1 || updatedRelatedTitles[0] != "Blue Lock Alt" {
		t.Fatalf("expected relatedTitles to be persisted and returned, got %v", updated["relatedTitles"])
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

func TestTrackersRejectInvalidRatingStep(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	createBody := map[string]any{
		"title":              "Invalid Rating",
		"sourceId":           1,
		"sourceUrl":          "https://mangadex.org/title/invalid-rating",
		"status":             "reading",
		"lastReadChapter":    1.0,
		"rating":             8.3,
		"latestKnownChapter": 2.0,
	}
	body, _ := json.Marshal(createBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/trackers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.StatusCode)
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

func TestAPIQueryMatchesWordsInAnyOrderAndRelatedTitles(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	_, err := db.Exec(`
		INSERT INTO trackers (title, related_titles, source_id, source_item_id, source_url, status)
		VALUES (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?)
	`,
		"Solo Leveling", `["Solo Leveling Ragnarok"]`, 1, "solo-leveling-ragnarok-c739e802", "https://asuracomic.net/series/solo-leveling-26b0cf1b", "reading",
		"Tower of God", `["Tower of God"]`, 1, "tower-of-god-123", "https://www.webtoons.com/en/fantasy/tower-of-god/list?title_no=95", "reading",
	)
	if err != nil {
		t.Fatalf("seed trackers: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/trackers?status=reading&q=ragnarok+solo", nil)
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
		t.Fatalf("expected 1 filtered item, got %d", len(items))
	}

	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("filtered item has invalid type")
	}
	if title, _ := item["title"].(string); title != "Solo Leveling" {
		t.Fatalf("expected Solo Leveling, got %v", item["title"])
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
