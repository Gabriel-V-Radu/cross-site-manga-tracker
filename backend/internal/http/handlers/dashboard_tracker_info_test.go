package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/metadata"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

func fetchPage(t *testing.T, app *fiber.App, target string) (int, string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil), 10000)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func seedInfoTracker(t *testing.T, db *sql.DB, title string, lastRead, latest float64) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (1, ?, 1, ?, 'reading', ?, ?)
	`, title, "https://mangadex.org/title/"+strings.ToLower(title), lastRead, latest)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

func saveConsolidated(t *testing.T, db *sql.DB, trackerID int64, view metadata.Consolidated) {
	t.Helper()
	payload, _ := json.Marshal(view)
	genres, _ := json.Marshal(view.Genres)
	if view.Genres == nil {
		genres = []byte("[]")
	}
	err := repository.NewMetadataRepository(db).SaveTrackerMetadata(context.Background(), []repository.TrackerMetadataRow{{
		TrackerID: trackerID, SeriesType: view.Type, SeriesStatus: view.Status, GenresJSON: string(genres),
		Payload: string(payload), Signature: "test", UpdatedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("save consolidated: %v", err)
	}
}

func TestTrackerInfoRendersStoredMetadata(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	id := seedInfoTracker(t, db, "Nano Machine", 10, 20)
	saveConsolidated(t, db, id, metadata.Consolidated{
		Type:              "manhwa",
		Status:            "ongoing",
		Year:              2020,
		Genres:            []string{"Action", "Martial Arts"},
		Tags:              []string{"Regression"},
		Description:       "First paragraph.\n\nSecond <b>paragraph</b>.",
		DescriptionSource: "MangaDex",
		Authors:           []string{"Hanjung Wolya"},
		Ratings:           []metadata.SourceRating{{SourceKey: "mangaupdates", SourceName: "MangaUpdates", Rating: 8.02, Votes: 603}},
		Sources:           []string{"MangaUpdates", "MangaDex"},
	})

	status, html := fetchPage(t, app, "/dashboard/trackers/"+toString(int(id))+"/info")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, html)
	}
	for _, want := range []string{
		"Nano Machine", ">Manhwa<", ">Ongoing<", "2020",
		`data-filter-name="genres" data-filter-value="Martial Arts"`,
		"Regression", "Hanjung Wolya", "★ 8.0", "603 votes",
		"<p>First paragraph.</p>", "Second &lt;b&gt;paragraph&lt;/b&gt;.",
		"Info from MangaUpdates, MangaDex", `href="https://mangadex.org/title/nano%20machine"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("info panel missing %q", want)
		}
	}
}

func TestTrackerInfoWithoutMetadataSaysSo(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedInfoTracker(t, db, "Unknown", 1, 2)

	status, html := fetchPage(t, app, "/dashboard/trackers/"+toString(int(id))+"/info")
	if status != http.StatusOK || !strings.Contains(html, "No series info yet") {
		t.Fatalf("status %d, body %s", status, html)
	}
	if status, _ := fetchPage(t, app, "/dashboard/trackers/999999/info"); status != http.StatusNotFound {
		t.Fatalf("unknown tracker: status %d", status)
	}
}

func TestTrackersPartialFiltersByGenreAndKeepsCaughtUpReading(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	behind := seedInfoTracker(t, db, "Behind", 1, 5)
	caughtUp := seedInfoTracker(t, db, "Caught", 5, 5)
	other := seedInfoTracker(t, db, "Elsewhere", 1, 5)
	saveConsolidated(t, db, behind, metadata.Consolidated{Type: "manhwa", Genres: []string{"Action"}})
	saveConsolidated(t, db, caughtUp, metadata.Consolidated{Type: "manhwa", Genres: []string{"Action"}})
	saveConsolidated(t, db, other, metadata.Consolidated{Type: "manga", Genres: []string{"Romance"}})

	_, plain := fetchPage(t, app, "/dashboard/trackers?status=reading")
	if !strings.Contains(plain, "Behind") || strings.Contains(plain, ">Caught<") {
		t.Fatalf("Reading alone must list Behind and leave out the caught-up tracker")
	}

	_, filtered := fetchPage(t, app, "/dashboard/trackers?status=reading&genres=Action")
	if !strings.Contains(filtered, ">Behind<") || !strings.Contains(filtered, ">Caught<") || strings.Contains(filtered, ">Elsewhere<") {
		t.Fatalf("Reading + genre must list both Action titles, caught-up included, and nothing else:\n%s", filtered)
	}

	_, typed := fetchPage(t, app, "/dashboard/trackers?status=all&types=manga")
	if !strings.Contains(typed, ">Elsewhere<") || strings.Contains(typed, ">Behind<") {
		t.Fatalf("type filter did not narrow to manga")
	}
}

func TestCardShowsWhenALateChapterWasFound(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	released := time.Now().UTC().Add(-21 * 24 * time.Hour)
	found := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter, latest_release_at, latest_chapter_found_at)
		VALUES (1, 'Late Extra', 1, 'https://mangadex.org/title/late-extra', 'reading', 1, 2, ?, ?),
		       (1, 'On Time', 1, 'https://mangadex.org/title/on-time', 'reading', 1, 2, ?, ?)
	`, released, found, found.Add(-3*time.Hour), found.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	_, html := fetchPage(t, app, "/dashboard/trackers?status=reading")
	if !strings.Contains(html, "found 2 hours ago") || !strings.Contains(html, "stat-value--found") {
		t.Fatalf("a chapter found three weeks after its release must say when it was found:\n%s", html)
	}
	if strings.Count(html, "stat-value--found") != 1 {
		t.Fatalf("a chapter found within hours of its release keeps its release date")
	}
	if first, second := strings.Index(html, "Late Extra"), strings.Index(html, "On Time"); first < 0 || second < 0 || first > second {
		t.Fatalf("the late find must rank first in the latest-chapter sort")
	}
}

func TestDashboardPageRendersTheFiltersTheURLCarries(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedInfoTracker(t, db, "Remembered", 1, 2)
	saveConsolidated(t, db, id, metadata.Consolidated{Type: "manhwa", Genres: []string{"Action"}})

	status, html := fetchPage(t, app, "/dashboard?q=rem&status=all&sort=unread&page=3&types=manhwa&genres=Action&genres=Gone")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{
		`name="q" placeholder="Title" value="rem"`,
		`value="all" title="All statuses" selected`,
		`value="unread" title="Most chapters to read" selected`,
		`id="page-input" value="3"`,
		`name="types" value="manhwa" checked`,
		`name="genres" value="Action" checked`,
		`name="genres" value="Gone" checked`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard page missing %q", want)
		}
	}
	if strings.Contains(html, "Editorial Control Room") {
		t.Error("the header still carries the old title block")
	}
}

func TestCardsShowUnreadCountAndSortByIt(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	seedInfoTracker(t, db, "Few Behind", 8, 10)
	seedInfoTracker(t, db, "Far Behind", 10, 52.5)
	seedInfoTracker(t, db, "Caught Up", 7, 7)

	_, html := fetchPage(t, app, "/dashboard/trackers?status=all&sort=unread")
	if !strings.Contains(html, `title="2 chapters to read">&#43;2<`) || !strings.Contains(html, `title="42.5 chapters to read">&#43;42.5<`) {
		t.Fatalf("cards must show how many chapters are left:\n%s", html)
	}
	if strings.Count(html, "tracker-card__unread") != 2 {
		t.Fatalf("a caught-up card shows no unread badge")
	}
	far, few, caught := strings.Index(html, ">Far Behind<"), strings.Index(html, ">Few Behind<"), strings.Index(html, ">Caught Up<")
	if far < 0 || few < 0 || caught < 0 || far > few || few > caught {
		t.Fatalf("most chapters to read must come first, caught-up last (far=%d few=%d caught=%d)", far, few, caught)
	}
}

func TestDashboardPageOffersMetadataFilters(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedInfoTracker(t, db, "Filtered", 1, 2)
	saveConsolidated(t, db, id, metadata.Consolidated{Type: "manhua", Status: "completed", Genres: []string{"Cultivation"}})

	status, html := fetchPage(t, app, "/dashboard")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{
		`name="types" value="manhua"`, `name="publication" value="completed"`, `name="genres" value="Cultivation"`,
		`id="tracker-info-popover"`, `id="filters-reset"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard page missing %q", want)
		}
	}
	for _, gone := range []string{`data-view-mode`, `id="view-input"`} {
		if strings.Contains(html, gone) {
			t.Errorf("dashboard page still carries the list view: %q", gone)
		}
	}
}
