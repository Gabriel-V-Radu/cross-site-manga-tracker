package handlers

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

// newSiteMetadataHandler builds a handler around the connector set the app
// actually runs, which is where the site metadata below now comes from: these
// tests exist to catch a connector that stops claiming a domain or moves its
// home page, so a stub registry would test nothing.
func newSiteMetadataHandler() *DashboardHandler {
	return &DashboardHandler{registry: connectordefaults.NewRegistry()}
}

func TestHasResolvedSourceMetadataRequiresReleaseDate(t *testing.T) {
	chapter := 42.0
	itemID := "mangadex-item"
	now := time.Now().UTC()

	trackerWithoutRelease := &models.Tracker{
		SourceItemID:       &itemID,
		LatestKnownChapter: &chapter,
	}
	if hasResolvedSourceMetadata(trackerWithoutRelease) {
		t.Fatalf("expected metadata without latest release date to be incomplete")
	}

	trackerWithRelease := &models.Tracker{
		SourceItemID:       &itemID,
		LatestKnownChapter: &chapter,
		LatestReleaseAt:    &now,
	}
	if !hasResolvedSourceMetadata(trackerWithRelease) {
		t.Fatalf("expected metadata with source item id, latest chapter, and release date to be complete")
	}
}

func TestSourceHomeURLForKeySupportsMgeko(t *testing.T) {
	homeURL := newSiteMetadataHandler().sourceHomeURLForKey("mgeko")
	if homeURL != "https://www.mgeko.cc" {
		t.Fatalf("expected mgeko home url, got %q", homeURL)
	}
}

func TestSourceKeyForURLSupportsMgeko(t *testing.T) {
	inferred := newSiteMetadataHandler().sourceKeyForURL("https://www.mgeko.cc/manga/sample-series/")
	if inferred != "mgeko" {
		t.Fatalf("expected inferred source key mgeko, got %q", inferred)
	}
}

func TestSourceHomeURLForKeySupportsFreeWebNovel(t *testing.T) {
	homeURL := newSiteMetadataHandler().sourceHomeURLForKey("freewebnovel")
	if homeURL != "https://freewebnovel.com" {
		t.Fatalf("expected freewebnovel home url, got %q", homeURL)
	}
}

func TestSourceKeyForURLSupportsFreeWebNovel(t *testing.T) {
	inferred := newSiteMetadataHandler().sourceKeyForURL("https://freewebnovel.com/novel/star-odyssey")
	if inferred != "freewebnovel" {
		t.Fatalf("expected inferred source key freewebnovel, got %q", inferred)
	}
}

// TestSourceKeyForURLKnowsEveryChapterSource pins a gap that has now shipped
// twice (MangaBuddy, then ComicK/MangaHub): a connector gets wired everywhere
// except here, and then a resolved chapter link cannot be attributed to its
// site — which silently breaks the open-to-read button following the freshest
// source.
func TestSourceKeyForURLKnowsEveryChapterSource(t *testing.T) {
	handler := newSiteMetadataHandler()
	cases := map[string]string{
		"https://mangahub.io/manga/sample-series_142": "mangahub",
		"https://comick.dev/comic/kagura-bachi":       "comick",
		"https://mangafire.to/title/npozj-akanabe":    "mangafire",
	}
	for rawURL, want := range cases {
		if inferred := handler.sourceKeyForURL(rawURL); inferred != want {
			t.Fatalf("expected inferred source key %q for %s, got %q", want, rawURL, inferred)
		}
	}
}

// TestSourceKeyForURLRejectsALookalikeHost covers what the substring table this
// replaced could not: a host that merely reads like a site's name is not that
// site, and attributing a card to it would name the wrong reader.
func TestSourceKeyForURLRejectsALookalikeHost(t *testing.T) {
	handler := newSiteMetadataHandler()
	for _, rawURL := range []string{
		"https://asura-mirror.example/comics/sample",
		"https://comick.example/comic/sample",
	} {
		if inferred := handler.sourceKeyForURL(rawURL); inferred != "" {
			t.Fatalf("expected no source key for %s, got %q", rawURL, inferred)
		}
	}
}

// TestSourceKeyForURLWithoutARegistry covers the handlers the card builders
// construct by struct literal: no registry means nothing is attributed, not a
// panic.
func TestSourceKeyForURLWithoutARegistry(t *testing.T) {
	handler := &DashboardHandler{}
	if inferred := handler.sourceKeyForURL("https://mangahub.io/manga/sample-series_142"); inferred != "" {
		t.Fatalf("expected no source key without a registry, got %q", inferred)
	}
	if homeURL := handler.sourceHomeURLForKey("mangahub"); homeURL != "" {
		t.Fatalf("expected no home url without a registry, got %q", homeURL)
	}
}

func TestBuildTrackerCardsDoesNotUseLastCheckedAtAsReleaseDate(t *testing.T) {
	lastCheckedAt := time.Now().UTC()
	h := newCardTestHandler(nil, fakeCoverResolver{}, fakeChapterLinkResolver{})
	items := []models.Tracker{{
		ID:            1,
		Title:         "Sample",
		Status:        "reading",
		SourceID:      1,
		SourceURL:     "https://example.com/series/sample",
		LastCheckedAt: &lastCheckedAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, pending := h.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, "")
	if pending {
		t.Fatalf("expected no asynchronous lookups for source without connector key")
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "—" {
		t.Fatalf("expected unknown release date marker, got %q", cards[0].LatestReleaseAgo)
	}
}

// TestBuildTrackerCardsShowsChapterSeenAtAsApproximateRelease covers the sources
// that report a chapter number and no date for it, where the card used to show only
// "—": it now falls back to when the chapter was first recorded here, marked as
// approximate so a detection time is never read as the site's release time.
func TestBuildTrackerCardsShowsChapterSeenAtAsApproximateRelease(t *testing.T) {
	chapter := 20.0
	seenAt := time.Now().UTC().Add(-48 * time.Hour)
	h := newCardTestHandler(nil, fakeCoverResolver{}, fakeChapterLinkResolver{})
	items := []models.Tracker{{
		ID:                  1,
		Title:               "Sample",
		Status:              "completed",
		SourceID:            1,
		SourceURL:           "https://example.com/series/sample",
		LatestKnownChapter:  &chapter,
		LatestChapterSeenAt: &seenAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, _ := h.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, "")
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "~2 days ago" {
		t.Fatalf("expected approximate release date from first-seen timestamp, got %q", cards[0].LatestReleaseAgo)
	}
	if !cards[0].LatestReleaseApproximate {
		t.Fatalf("expected the release date to be marked approximate")
	}
	if cards[0].LatestReleaseTitle == "" {
		t.Fatalf("expected an explanation of where the approximate date came from")
	}
}

// countingChapterLinkResolver degrades every chapter link to the series page,
// reports the lookup pending or not as the test chooses, and records which
// links it is asked to invalidate. Guarded because the handler runs on fiber's
// goroutine while the test flips and reads it between requests.
type countingChapterLinkResolver struct {
	mu          sync.Mutex
	waiting     bool
	invalidated []string
}

func (r *countingChapterLinkResolver) Lookup(_ string, sourceURL string, _ float64, _ []repository.TrackerSourceRef, _ string) (string, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sourceURL, false, r.waiting
}

func (r *countingChapterLinkResolver) InvalidateSource(sourceKey, sourceURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, sourceKey+" "+sourceURL)
}

func (r *countingChapterLinkResolver) Close() {}

func (r *countingChapterLinkResolver) setWaiting(waiting bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waiting = waiting
}

// takeInvalidations returns the links named since the last call, each once and
// in order, as "source-key series-url".
func (r *countingChapterLinkResolver) takeInvalidations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	named := slices.Compact(slices.Sorted(slices.Values(r.invalidated)))
	r.invalidated = nil
	return named
}

// recordingCoverResolver answers every cover lookup with nothing and records
// which covers' negatives it is asked to invalidate, as "source-key
// series-url". Guarded for the same reason the chapter double is.
type recordingCoverResolver struct {
	fakeCoverResolver
	mu          sync.Mutex
	invalidated []string
}

func (r *recordingCoverResolver) InvalidateNegative(sourceKey, sourceURL string, _ *string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, sourceKey+" "+sourceURL)
}

func (r *recordingCoverResolver) takeInvalidations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	named := slices.Compact(slices.Sorted(slices.Values(r.invalidated)))
	r.invalidated = nil
	return named
}

// newEditTestHandler builds a real handler over a migrated database, with the
// two resolvers swapped for the recording doubles, so a save's effect on the
// caches can be observed without a connector or the network. Only the routes
// the tests drive are mounted.
func newEditTestHandler(t *testing.T) (*sql.DB, *fiber.App, *countingChapterLinkResolver, *recordingCoverResolver) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	_, currentFile, _, _ := runtime.Caller(0)
	webDir := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "web")
	handler := NewDashboardHandler(db, connectors.NewRegistry(), DashboardPaths{
		TemplatesGlob: filepath.Join(webDir, "templates", "*.html"),
		AssetsDir:     filepath.Join(webDir, "assets"),
	})
	if err := handler.TemplateError(); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	handler.covers.Close()
	handler.chapterLinks.Close()
	chapters := &countingChapterLinkResolver{}
	covers := &recordingCoverResolver{}
	handler.covers = covers
	handler.chapterLinks = chapters
	t.Cleanup(handler.Close)

	app := fiber.New()
	app.Post("/dashboard/trackers/:id", handler.UpdateFromForm)
	app.Get("/dashboard/trackers/:id/card-fragment", handler.CardFragment)
	app.Post("/dashboard/links/suggestions/:id/accept", handler.AcceptLinkSuggestion)
	return db, app, chapters, covers
}

// seedEditTracker stores a MangaDex tracker with a second, MangaFire link, the
// state an edit modal opens on.
func seedEditTracker(t *testing.T, db *sql.DB) (trackerID int64, mangafireID int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (1, 'Edited Tracker', 1, 'https://mangadex.org/title/edited', 'reading', 5, 10)
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ = result.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, 1, ?), (?, ?, ?)
	`, trackerID, "https://mangadex.org/title/edited", trackerID, mangafireID, "https://mangafire.to/manga/edited.x1"); err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}
	return trackerID, mangafireID
}

// editForm is the edit modal submitted exactly as it rendered.
func editForm(mangafireID int64) url.Values {
	form := url.Values{}
	form.Set("title", "Edited Tracker")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/edited")
	form.Set("status", "reading")
	form.Set("last_read_chapter", "5")
	form.Set("latest_known_chapter", "10")
	form.Set("reading_source_id", "0")
	form.Set("linked_sources_json", `[{"sourceId":1,"sourceUrl":"https://mangadex.org/title/edited"},`+
		`{"sourceId":`+strconv.FormatInt(mangafireID, 10)+`,"sourceUrl":"https://mangafire.to/manga/edited.x1"}]`)
	return form
}

func postEditForm(t *testing.T, app *fiber.App, trackerID int64, form url.Values) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("edit request failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}
	return string(body)
}

// TestUpdateFromFormInvalidatesLinksOnlyWhenTheyChanged pins that a save drops
// cached chapter links only when it changed what those links were worked out
// against, and then only the saved tracker's. Every save used to drop the
// whole cache — a status edit included — which emptied the library's chapter
// links and left the saved card on its series page; and until the resolvers
// could be told which links, a save that did change them still emptied it for
// every other tracker. The second tracker seeded here is never to be named.
func TestUpdateFromFormInvalidatesLinksOnlyWhenTheyChanged(t *testing.T) {
	db, app, chapters, covers := newEditTestHandler(t)
	trackerID, mangafireID := seedEditTracker(t, db)
	if _, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'Bystander', 1, 'https://mangadex.org/title/bystander', 'reading')
	`); err != nil {
		t.Fatalf("seed the second tracker: %v", err)
	}

	const mangadexLink = "mangadex https://mangadex.org/title/edited"
	const mangafireLink = "mangafire https://mangafire.to/manga/edited.x1"
	bothLinks := []string{mangadexLink, mangafireLink}

	cases := []struct {
		name  string
		edit  func(url.Values)
		wants []string
	}{
		{name: "status and title only", edit: func(form url.Values) {
			form.Set("status", "on_hold")
			form.Set("title", "Renamed Tracker")
		}},
		{name: "chapter numbers only", edit: func(form url.Values) {
			form.Set("latest_known_chapter", "11")
		}},
		{name: "reading pin set", edit: func(form url.Values) {
			form.Set("reading_source_id", strconv.FormatInt(mangafireID, 10))
		}, wants: bothLinks},
		{name: "reading pin cleared", edit: func(url.Values) {}, wants: bothLinks},
		// The link is gone from the tracker by the time the caches are told,
		// and is named all the same: a pinned lookup started from it.
		{name: "a linked site removed", edit: func(form url.Values) {
			form.Set("linked_sources_json", `[{"sourceId":1,"sourceUrl":"https://mangadex.org/title/edited"}]`)
		}, wants: bothLinks},
	}

	// The cases run in order against one tracker: each starts from what the
	// previous one saved, which is how the pin gets set and then cleared.
	for _, testCase := range cases {
		form := editForm(mangafireID)
		testCase.edit(form)
		postEditForm(t, app, trackerID, form)
		if got := chapters.takeInvalidations(); !slices.Equal(got, testCase.wants) {
			t.Fatalf("%s: invalidated %q, want %q", testCase.name, got, testCase.wants)
		}
		// The same links' "no cover", and nobody else's: this used to drop
		// every negative cover in memory and in the database.
		if got := covers.takeInvalidations(); !slices.Equal(got, testCase.wants) {
			t.Fatalf("%s: invalidated the covers of %q, want %q", testCase.name, got, testCase.wants)
		}
	}
}

// TestAcceptingALinkInvalidatesOnlyThatTrackersLookups pins the same scoping
// for the link review's accept: the tracker's links are named — the one just
// accepted among them, read back after the write — and the tracker beside it
// is not. One accepted candidate used to cost every card its chapter links.
func TestAcceptingALinkInvalidatesOnlyThatTrackersLookups(t *testing.T) {
	db, app, chapters, covers := newEditTestHandler(t)
	trackerID, _ := seedEditTracker(t, db)
	if _, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'Bystander', 1, 'https://mangadex.org/title/bystander', 'reading')
	`); err != nil {
		t.Fatalf("seed the second tracker: %v", err)
	}
	var comickID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO source_link_suggestions (tracker_id, source_id, candidate_url, candidate_title, score, status)
		VALUES (?, ?, 'https://comick.dev/comic/edited', 'Edited Tracker', 1.0, 'pending')
	`, trackerID, comickID)
	if err != nil {
		t.Fatalf("seed suggestion: %v", err)
	}
	suggestionID, _ := result.LastInsertId()

	req := httptest.NewRequest(http.MethodPost, "/dashboard/links/suggestions/"+strconv.FormatInt(suggestionID, 10)+"/accept", nil)
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("accept request failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	want := []string{
		"comick https://comick.dev/comic/edited",
		"mangadex https://mangadex.org/title/edited",
		"mangafire https://mangafire.to/manga/edited.x1",
	}
	if got := chapters.takeInvalidations(); !slices.Equal(got, want) {
		t.Fatalf("invalidated the chapter links of %q, want %q", got, want)
	}
	if got := covers.takeInvalidations(); !slices.Equal(got, want) {
		t.Fatalf("invalidated the covers of %q, want %q", got, want)
	}
}

// TestSingleCardRefreshFollowsPendingLookups pins how a swapped card that is
// still waiting on a lookup gets corrected: the response schedules a re-fetch
// of the card's fragment, and the fragment says whether to ask again. Nothing
// re-rendered such a card before, so it kept its series-page links.
func TestSingleCardRefreshFollowsPendingLookups(t *testing.T) {
	db, app, chapters, _ := newEditTestHandler(t)
	trackerID, mangafireID := seedEditTracker(t, db)
	fragmentPath := "/dashboard/trackers/" + strconv.FormatInt(trackerID, 10) + "/card-fragment?view=grid&profile=profile1"

	for _, waiting := range []bool{true, false} {
		chapters.setWaiting(waiting)

		body := postEditForm(t, app, trackerID, editForm(mangafireID))
		if !strings.Contains(body, `hx-swap-oob="outerHTML:#tracker-card-`+strconv.FormatInt(trackerID, 10)+`"`) {
			t.Fatalf("waiting=%v: expected the card to be swapped in", waiting)
		}
		if scheduled := strings.Contains(body, cardLookupsPendingHeader); scheduled != waiting {
			t.Fatalf("waiting=%v: refresh scheduled = %v", waiting, scheduled)
		}

		res, err := app.Test(httptest.NewRequest(http.MethodGet, fragmentPath, nil), 10000)
		if err != nil {
			t.Fatalf("fragment request failed: %v", err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("waiting=%v: fragment status %d", waiting, res.StatusCode)
		}
		if got, want := res.Header.Get(cardLookupsPendingHeader) == "1", waiting; got != want {
			t.Fatalf("waiting=%v: fragment pending header = %q", waiting, res.Header.Get(cardLookupsPendingHeader))
		}
	}
}

// TestBuildTrackerCardsPrefersReportedReleaseDate keeps the fallback above from
// overriding a date a source actually reported.
func TestBuildTrackerCardsPrefersReportedReleaseDate(t *testing.T) {
	chapter := 20.0
	releasedAt := time.Now().UTC().Add(-2 * time.Hour)
	seenAt := time.Now().UTC().Add(-48 * time.Hour)
	h := newCardTestHandler(nil, fakeCoverResolver{}, fakeChapterLinkResolver{})
	items := []models.Tracker{{
		ID:                  1,
		Title:               "Sample",
		Status:              "reading",
		SourceID:            1,
		SourceURL:           "https://example.com/series/sample",
		LatestKnownChapter:  &chapter,
		LatestReleaseAt:     &releasedAt,
		LatestChapterSeenAt: &seenAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, _ := h.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, "")
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "2 hours ago" {
		t.Fatalf("expected the reported release date, got %q", cards[0].LatestReleaseAgo)
	}
	if cards[0].LatestReleaseApproximate {
		t.Fatalf("did not expect a reported release date to be marked approximate")
	}
}
