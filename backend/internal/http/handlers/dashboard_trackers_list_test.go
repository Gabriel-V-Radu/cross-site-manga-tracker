package handlers_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

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

func TestDashboardSortByRating(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	_, err := db.Exec(`
		INSERT INTO trackers (title, source_id, source_url, status, rating)
		VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)
	`,
		"Rating Sort Low", 1, "https://mangadex.org/title/rating-sort-low", "completed", 2.5,
		"Rating Sort High", 1, "https://mangadex.org/title/rating-sort-high", "completed", 9.5,
		"Rating Sort None", 1, "https://mangadex.org/title/rating-sort-none", "completed", nil,
	)
	if err != nil {
		t.Fatalf("seed trackers: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?status=all&sort=rating&order=desc", nil)
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

	highIndex := strings.Index(html, "Rating Sort High")
	lowIndex := strings.Index(html, "Rating Sort Low")
	noneIndex := strings.Index(html, "Rating Sort None")
	if highIndex < 0 || lowIndex < 0 || noneIndex < 0 {
		t.Fatalf("expected all seeded trackers in response")
	}
	if !(highIndex < lowIndex && lowIndex < noneIndex) {
		t.Fatalf("expected rating desc order High -> Low -> None")
	}
}

func TestDashboardSearchMatchesWordsInAnyOrderAndRelatedTitles(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?status=all&q=ragnarok+solo", nil)
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

	if !strings.Contains(html, "Solo Leveling") {
		t.Fatalf("expected search to match related_titles tokens in any order")
	}
	if strings.Contains(html, "Tower of God") {
		t.Fatalf("did not expect unrelated tracker in search results")
	}
}
