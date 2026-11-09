package handlers_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// editOutageConnector is one linked site as a links-changing save sees it: it
// answers with a fixed reading, answers with no chapter number, or fails like
// a site behind a challenge. It claims its host like every real connector, so
// the form's source realignment treats it the same, and it holds no mutable
// state: the card render after the save may call it from background lookups.
type editOutageConnector struct {
	key  string
	host string
	// down fails every request; otherwise chapter (nil for "no number") and
	// the fixed release date are what the site reports.
	down    bool
	chapter *float64
}

// editOutageAnswerDate is the release date every answering site reports,
// distinct from the one the tracker is seeded with.
var editOutageAnswerDate = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func (e editOutageConnector) Key() string     { return e.key }
func (e editOutageConnector) Name() string    { return e.key }
func (e editOutageConnector) Kind() string    { return connectors.KindNative }
func (e editOutageConnector) Hosts() []string { return []string{e.host} }
func (e editOutageConnector) HomeURL() string { return "https://" + e.host }
func (e editOutageConnector) ReaderRank() int { return connectors.ReaderRankDefault }
func (e editOutageConnector) HealthCheck(context.Context) error {
	if e.down {
		return errors.New("blocked")
	}
	return nil
}
func (e editOutageConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (e editOutageConnector) ResolveByURL(_ context.Context, rawURL string) (*connectors.MangaResult, error) {
	if e.down {
		return nil, errors.New("behind a browser challenge")
	}
	result := &connectors.MangaResult{SourceKey: e.key, URL: rawURL}
	if e.chapter != nil {
		chapter := *e.chapter
		releasedAt := editOutageAnswerDate
		result.LatestChapter = &chapter
		result.LastUpdatedAt = &releasedAt
	}
	return result, nil
}

// The three sites of these tests and the link each tracker has on them. By
// site name — the order the edit modal lists links in — ComicK comes first and
// MangaFire, the stored primary, second.
var editOutageSites = []struct{ key, host, url string }{
	{"comick", "comick.io", "https://comick.io/comic/outage"},
	{"mangafire", "mangafire.to", "https://mangafire.to/title/outage"},
	{"mangahub", "mangahub.io", "https://mangahub.io/manga/outage"},
}

// siteAnswer is how one site behaves during a save.
type siteAnswer struct {
	down    bool
	chapter *float64
}

func answers(chapter float64) siteAnswer { return siteAnswer{chapter: &chapter} }

var (
	siteDown          = siteAnswer{down: true}
	answersNoChapters = siteAnswer{}
)

// editOutageResult is the tracker row after the save.
type editOutageResult struct {
	sourceKey string
	sourceURL string
	latest    sql.NullFloat64
	releaseAt sql.NullTime
	links     []string
}

// editOutageSeedRelease is the stored chapter's release date before the save.
var editOutageSeedRelease = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// saveLinksDuringOutage seeds a tracker on chapter 100 whose primary is
// MangaFire and which is also linked to ComicK, then saves the edit form with
// the given links (site keys, in the order given) while the sites behave as
// behaviour says. The stored links are MangaFire and ComicK, so any other list
// is a links-changing save and re-resolves every link.
func saveLinksDuringOutage(t *testing.T, behaviour map[string]siteAnswer, submitted []string, extraFields map[string]string) editOutageResult {
	t.Helper()
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		for _, site := range editOutageSites {
			answer := behaviour[site.key]
			if err := registry.Register(editOutageConnector{key: site.key, host: site.host, down: answer.down, chapter: answer.chapter}); err != nil {
				t.Fatalf("register %s: %v", site.key, err)
			}
		}
	})
	defer cleanup()

	sourceIDs := map[string]int64{}
	siteURLs := map[string]string{}
	for _, site := range editOutageSites {
		var id int64
		if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, site.key).Scan(&id); err != nil {
			t.Fatalf("look up %s source: %v", site.key, err)
		}
		sourceIDs[site.key] = id
		siteURLs[site.key] = site.url
	}

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter, latest_release_at)
		VALUES (1, 'Outage Series', ?, ?, 'reading', 100, ?)
	`, sourceIDs["mangafire"], siteURLs["mangafire"], editOutageSeedRelease)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ := result.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?), (?, ?, ?)
	`, trackerID, sourceIDs["mangafire"], siteURLs["mangafire"],
		trackerID, sourceIDs["comick"], siteURLs["comick"]); err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	entries := make([]string, 0, len(submitted))
	for _, key := range submitted {
		entries = append(entries, `{"sourceId":`+strconv.FormatInt(sourceIDs[key], 10)+`,"sourceUrl":"`+siteURLs[key]+`"}`)
	}
	// The primary fields as the modal renders them: the stored primary.
	form := url.Values{}
	form.Set("title", "Outage Series")
	form.Set("source_id", strconv.FormatInt(sourceIDs["mangafire"], 10))
	form.Set("source_url", siteURLs["mangafire"])
	form.Set("status", "reading")
	form.Set("latest_known_chapter", "100")
	form.Set("latest_known_chapter_original", "100")
	form.Set("linked_sources_json", "["+strings.Join(entries, ",")+"]")
	for key, value := range extraFields {
		form.Set(key, value)
	}

	res := postForm(t, app, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10), form, nil)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, body)
	}

	var saved editOutageResult
	if err := db.QueryRow(`
		SELECT s.key, t.source_url, t.latest_known_chapter, t.latest_release_at
		FROM trackers t INNER JOIN sources s ON s.id = t.source_id
		WHERE t.id = ?
	`, trackerID).Scan(&saved.sourceKey, &saved.sourceURL, &saved.latest, &saved.releaseAt); err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	rows, err := db.Query(`
		SELECT s.key FROM tracker_sources ts INNER JOIN sources s ON s.id = ts.source_id
		WHERE ts.tracker_id = ? ORDER BY s.key
	`, trackerID)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan link: %v", err)
		}
		saved.links = append(saved.links, key)
	}
	return saved
}

func (r editOutageResult) assertPrimary(t *testing.T, wantKey string) {
	t.Helper()
	wantURL := ""
	for _, site := range editOutageSites {
		if site.key == wantKey {
			wantURL = site.url
		}
	}
	if r.sourceKey != wantKey || r.sourceURL != wantURL {
		t.Fatalf("primary = %s (%s), want %s (%s)", r.sourceKey, r.sourceURL, wantKey, wantURL)
	}
}

func (r editOutageResult) assertChapter(t *testing.T, wantChapter float64, wantRelease time.Time) {
	t.Helper()
	if !r.latest.Valid || r.latest.Float64 != wantChapter {
		t.Fatalf("latest_known_chapter = %+v, want %v", r.latest, wantChapter)
	}
	if !r.releaseAt.Valid || !r.releaseAt.Time.Equal(wantRelease) {
		t.Fatalf("latest_release_at = %+v, want %s", r.releaseAt, wantRelease)
	}
}

func (r editOutageResult) assertLinks(t *testing.T, want ...string) {
	t.Helper()
	if strings.Join(r.links, ",") != strings.Join(want, ",") {
		t.Fatalf("links = %v, want %v", r.links, want)
	}
}

// A links-changing save re-resolves every link. A site that does not answer
// used to lose its place to the best one that did: adding a MangaHub link
// while MangaFire sat behind its challenge made ComicK or MangaHub the primary
// and stored the lower chapter they were on. The outage now changes nothing
// about the primary, and a partial set of answers cannot lower the chapter.
func TestEditKeepsThePrimaryThatFailedToAnswer(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick":    answers(95),
		"mangafire": siteDown,
		"mangahub":  answers(96),
	}, []string{"comick", "mangafire", "mangahub"}, nil)

	saved.assertPrimary(t, "mangafire")
	saved.assertChapter(t, 100, editOutageSeedRelease)
	saved.assertLinks(t, "comick", "mangafire", "mangahub")
}

// The outage protects the primary and the stored number, not the tracker from
// news: a site that did answer with a newer chapter is still believed.
func TestEditDuringAnOutageStillTakesAHigherChapter(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick":    answers(101),
		"mangafire": siteDown,
		"mangahub":  answers(96),
	}, []string{"comick", "mangafire", "mangahub"}, nil)

	saved.assertPrimary(t, "mangafire")
	saved.assertChapter(t, 101, editOutageAnswerDate)
}

// With every site down the list's first entry used to become the primary —
// the list is ordered by site name, so ComicK took over from MangaFire. The
// current primary stays while it is still linked.
func TestEditWithNothingAnsweringKeepsTheCurrentPrimary(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick": siteDown, "mangafire": siteDown, "mangahub": siteDown,
	}, []string{"comick", "mangafire", "mangahub"}, nil)

	saved.assertPrimary(t, "mangafire")
	saved.assertChapter(t, 100, editOutageSeedRelease)
}

// Only a primary the reader unlinked gives way with nothing answering, and
// then to the first entry of the list.
func TestEditWithNothingAnsweringAndThePrimaryUnlinkedTakesTheFirstEntry(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick": siteDown, "mangafire": siteDown, "mangahub": siteDown,
	}, []string{"mangahub", "comick"}, map[string]string{
		// The modal's primary fields follow the first remaining link.
		"source_url": "https://mangahub.io/manga/outage",
	})

	saved.assertPrimary(t, "mangahub")
	saved.assertChapter(t, 100, editOutageSeedRelease)
	saved.assertLinks(t, "comick", "mangahub")
}

// With every linked site answering the save has the whole picture and behaves
// as it always has: the best reading becomes the primary and its number is
// stored, lower than the stored one or not.
func TestEditWithEverySiteAnsweringStoresTheBestReading(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick":    answers(95),
		"mangafire": answers(90),
		"mangahub":  answers(93),
	}, []string{"comick", "mangafire", "mangahub"}, nil)

	saved.assertPrimary(t, "comick")
	saved.assertChapter(t, 95, editOutageAnswerDate)
}

// The protection is for a primary that failed. One that answered competes
// like any other site, so a better reading still takes the primary — but with
// a third site silent the answers are incomplete, and the stored chapter is
// not lowered to theirs.
func TestEditWithAnAlternateDownStillRanksThePrimaryThatAnswered(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick":    answers(99),
		"mangafire": answers(98),
		"mangahub":  siteDown,
	}, []string{"comick", "mangafire", "mangahub"}, nil)

	saved.assertPrimary(t, "comick")
	saved.assertChapter(t, 100, editOutageSeedRelease)
}

// A chapter the reader typed in this same save is not lowered by incomplete
// answers either, and a site that answered without any chapter number does
// not count as having failed.
func TestEditDuringAnOutageKeepsAHandSetChapter(t *testing.T) {
	saved := saveLinksDuringOutage(t, map[string]siteAnswer{
		"comick":    answers(110),
		"mangafire": siteDown,
		"mangahub":  answersNoChapters,
	}, []string{"comick", "mangafire", "mangahub"}, map[string]string{
		"latest_known_chapter": "120",
	})

	saved.assertPrimary(t, "mangafire")
	if !saved.latest.Valid || saved.latest.Float64 != 120 {
		t.Fatalf("latest_known_chapter = %+v, want the 120 the reader typed", saved.latest)
	}
}

// Saving the form for its title sends the same links back. That used to
// recreate every tracker_sources row, wiping when the poll rotation last asked
// each site and whether the site was in step.
func TestEditThatLeavesTheLinksAloneKeepsThePollRotationMemory(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	var mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (1, 'Rotating Series', 1, 'https://mangadex.org/title/rotating', 'reading', 40)
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ := result.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url, in_step, last_polled_at) VALUES
			(?, 1, 'https://mangadex.org/title/rotating', 1, '2026-11-07 09:00:00'),
			(?, ?, 'https://mangafire.to/title/rotating', 1, '2026-11-07 10:00:00')
	`, trackerID, trackerID, mangafireID); err != nil {
		t.Fatalf("seed tracker sources: %v", err)
	}

	form := url.Values{}
	form.Set("title", "Rotating Series Renamed")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/rotating")
	form.Set("status", "reading")
	form.Set("latest_known_chapter", "40")
	form.Set("linked_sources_json", `[{"sourceId":1,"sourceUrl":"https://mangadex.org/title/rotating"},`+
		`{"sourceId":`+strconv.FormatInt(mangafireID, 10)+`,"sourceUrl":"https://mangafire.to/title/rotating"}]`)
	res := postForm(t, app, "/dashboard/trackers/"+strconv.FormatInt(trackerID, 10), form, nil)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, body)
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM trackers WHERE id = ?`, trackerID).Scan(&title); err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if title != "Rotating Series Renamed" {
		t.Fatalf("title = %q, the save did not apply", title)
	}
	var links, remembered int
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(in_step = 1 AND last_polled_at IS NOT NULL), 0)
		FROM tracker_sources WHERE tracker_id = ?
	`, trackerID).Scan(&links, &remembered); err != nil {
		t.Fatalf("load links: %v", err)
	}
	if links != 2 || remembered != 2 {
		t.Fatalf("%d of %d links kept their rotation memory, want 2 of 2", remembered, links)
	}
}
