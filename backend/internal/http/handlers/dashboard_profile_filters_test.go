package handlers_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

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
		trackerID, 2, "replacement", "https://mangafire.to/manga/100",
	)
	if err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	linkedJSON := `[{"sourceId":2,"sourceItemId":"replacement","sourceUrl":"https://mangafire.to/manga/100"}]`
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
	if sourceURL != "https://mangafire.to/manga/100" {
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
		1, "Linked Sites Other", 2, "https://mangafire.to/manga/other", "reading", 1.0, 9.0,
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
		matchTrackerID, 2, "match-2", "https://mangafire.to/manga/match",
		partialTrackerID, 1, "partial-1", "https://mangadex.org/title/linked-sites-partial",
		otherTrackerID, 2, "other-2", "https://mangafire.to/manga/other",
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
		trackerID, 2, "partial-endpoint-2", "https://mangafire.to/manga/partial-endpoint",
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
