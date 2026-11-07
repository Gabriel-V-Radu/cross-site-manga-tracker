package handlers_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gofiber/fiber/v2"
)

func TestLinksQueueAcceptFlow(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	var comickID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}
	var mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (1, 'Nano Machine', ?, 'https://mangafire.to/manga/nano-machine', 'reading', 326)
	`, mangafireID)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ := result.LastInsertId()

	if _, err := db.Exec(`
		INSERT INTO source_link_suggestions
			(tracker_id, source_id, candidate_url, candidate_title, score, status)
		VALUES (?, ?, 'https://comick.io/comic/01J76XYDT7H7ANER8KJG5R9SJV', 'Nano Machine', 1.0, 'pending')
	`, trackerID, comickID); err != nil {
		t.Fatalf("seed suggestion: %v", err)
	}

	// The queue renders the pending candidate.
	queueReq := httptest.NewRequest("GET", "/dashboard/links/queue?source="+toString(int(comickID)), nil)
	queueRes, err := app.Test(queueReq, -1)
	if err != nil {
		t.Fatalf("queue request: %v", err)
	}
	queueBody, _ := io.ReadAll(queueRes.Body)
	if queueRes.StatusCode != 200 {
		t.Fatalf("queue status = %d: %s", queueRes.StatusCode, queueBody)
	}
	if !strings.Contains(string(queueBody), "Nano Machine") || !strings.Contains(string(queueBody), "Exact title") {
		t.Fatalf("queue does not show the candidate: %s", queueBody)
	}

	var suggestionID int64
	if err := db.QueryRow(`SELECT id FROM source_link_suggestions WHERE tracker_id = ?`, trackerID).Scan(&suggestionID); err != nil {
		t.Fatalf("read suggestion id: %v", err)
	}

	// Accepting links the tracker and removes the card.
	acceptReq := httptest.NewRequest("POST", "/dashboard/links/suggestions/"+toString(int(suggestionID))+"/accept", nil)
	acceptRes, err := app.Test(acceptReq, -1)
	if err != nil {
		t.Fatalf("accept request: %v", err)
	}
	acceptBody, _ := io.ReadAll(acceptRes.Body)
	if acceptRes.StatusCode != 200 {
		t.Fatalf("accept status = %d: %s", acceptRes.StatusCode, acceptBody)
	}
	if strings.Contains(string(acceptBody), "link-card-"+toString(int(trackerID))) {
		t.Fatalf("accepted tracker still renders a card: %s", acceptBody)
	}

	var linked int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = ?
	`, trackerID, comickID).Scan(&linked); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if linked != 1 {
		t.Fatalf("tracker_sources rows = %d, want 1", linked)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM source_link_suggestions WHERE id = ?`, suggestionID).Scan(&status); err != nil {
		t.Fatalf("read suggestion status: %v", err)
	}
	if status != "accepted" {
		t.Fatalf("suggestion status = %q, want accepted", status)
	}
}

func TestLinksDismissRemovesFromQueue(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	var comickID, mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'Obscure Series', ?, 'https://mangafire.to/manga/obscure', 'reading')
	`, mangafireID)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ := result.LastInsertId()

	form := url.Values{}
	dismissReq := httptest.NewRequest("POST",
		"/dashboard/links/trackers/"+toString(int(trackerID))+"/dismiss?source="+toString(int(comickID)),
		strings.NewReader(form.Encode()))
	dismissReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dismissRes, err := app.Test(dismissReq, -1)
	if err != nil {
		t.Fatalf("dismiss request: %v", err)
	}
	if dismissRes.StatusCode != 200 {
		body, _ := io.ReadAll(dismissRes.Body)
		t.Fatalf("dismiss status = %d: %s", dismissRes.StatusCode, body)
	}

	queueReq := httptest.NewRequest("GET", "/dashboard/links/queue?source="+toString(int(comickID)), nil)
	queueRes, err := app.Test(queueReq, -1)
	if err != nil {
		t.Fatalf("queue request: %v", err)
	}
	queueBody, _ := io.ReadAll(queueRes.Body)
	if strings.Contains(string(queueBody), "Obscure Series") {
		t.Fatalf("dismissed tracker still in queue: %s", queueBody)
	}
}

// unreachableConnector stands in for a site behind a browser challenge: it is
// registered, but every request fails. It publishes SiteInfo like every real
// connector because the registry maps a URL to a connector solely through the
// hosts each one claims; a double that skipped it would be unreachable by URL
// for a reason no production connector shares, and would test the wrong thing.
type unreachableConnector struct {
	key   string
	hosts []string
}

func (u unreachableConnector) Key() string     { return u.key }
func (u unreachableConnector) Name() string    { return u.key }
func (u unreachableConnector) Kind() string    { return connectors.KindNative }
func (u unreachableConnector) Hosts() []string { return u.hosts }
func (u unreachableConnector) ReaderRank() int { return connectors.ReaderRankDefault }
func (u unreachableConnector) HomeURL() string {
	if len(u.hosts) == 0 {
		return ""
	}
	return "https://" + u.hosts[0]
}
func (u unreachableConnector) HealthCheck(context.Context) error { return errors.New("blocked") }
func (u unreachableConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, errors.New("blocked")
}
func (u unreachableConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, errors.New("behind a browser challenge")
}

// healthCountingConnector is a healthy site that counts its health checks, to
// observe how often the "broken right now" sweep actually runs.
type healthCountingConnector struct {
	key    string
	checks *atomic.Int32
}

func (h healthCountingConnector) Key() string  { return h.key }
func (h healthCountingConnector) Name() string { return h.key }
func (h healthCountingConnector) Kind() string { return connectors.KindNative }
func (h healthCountingConnector) HealthCheck(context.Context) error {
	h.checks.Add(1)
	return nil
}
func (h healthCountingConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (h healthCountingConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, errors.New("not used")
}

func linksRequest(t *testing.T, app *fiber.App, method string, target string) (int, string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest(method, target, nil), -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// TestBrokenSourceSweepIsCachedWhenNothingIsBroken pins the cache for the
// usual answer, an empty one: it used to be stored as nil and never counted
// as cached, so every queue load re-checked every site. The cached empty set
// must still match nothing rather than widen to the whole library.
func TestBrokenSourceSweepIsCachedWhenNothingIsBroken(t *testing.T) {
	checks := &atomic.Int32{}
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		if err := registry.Register(healthCountingConnector{key: "comick", checks: checks}); err != nil {
			t.Fatalf("register connector: %v", err)
		}
	})
	defer cleanup()

	var comickID, mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'Unlinked Series', ?, 'https://mangafire.to/manga/unlinked', 'reading')
	`, mangafireID); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	queueURL := "/dashboard/links/queue?source=" + toString(int(comickID)) + "&primary=broken"
	for attempt := 1; attempt <= 2; attempt++ {
		status, body := linksRequest(t, app, "GET", queueURL)
		if status != 200 {
			t.Fatalf("queue load %d status = %d: %s", attempt, status, body)
		}
		if strings.Contains(body, "Unlinked Series") {
			t.Fatalf("queue load %d listed a tracker whose primary is not broken: %s", attempt, body)
		}
	}
	if got := checks.Load(); got != 1 {
		t.Fatalf("health checks = %d over two queue loads, want 1: the empty sweep was not cached", got)
	}
}

// TestLinkScanStatusRefreshesQueueOnlyFromRunningPoll pins the queue refresh
// after a scan: any poll from the running status that finds the scan over
// carries it, however late it arrives, and the page-load request never does.
func TestLinkScanStatusRefreshesQueueOnlyFromRunningPoll(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	const queueRefresh = `hx-get="/dashboard/links/queue"`

	status, body := linksRequest(t, app, "GET", "/dashboard/links/scan-status")
	if status != 200 {
		t.Fatalf("page-load status = %d: %s", status, body)
	}
	if strings.Contains(body, queueRefresh) {
		t.Fatalf("the page-load status request carried the queue refresh: %s", body)
	}

	status, body = linksRequest(t, app, "GET", "/dashboard/links/scan-status?poll=1")
	if status != 200 {
		t.Fatalf("poll status = %d: %s", status, body)
	}
	if !strings.Contains(body, queueRefresh) {
		t.Fatalf("a running-status poll that found no scan running did not refresh the queue: %s", body)
	}
}

// TestStartLinkScanExplainsARefusedStart pins that a Scan click that starts
// nothing says so instead of silently showing whatever status there was.
func TestStartLinkScanExplainsARefusedStart(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	// The test registry is empty, so the source exists but its connector does
	// not, and Start refuses.
	var comickID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}

	status, body := linksRequest(t, app, "POST", "/dashboard/links/scan?source="+toString(int(comickID)))
	if status != 200 {
		t.Fatalf("scan status = %d: %s", status, body)
	}
	if !strings.Contains(body, "The scan could not start") {
		t.Fatalf("a refused start rendered no notice: %s", body)
	}
}

// TestLinksPageKeepsItsProfileAndLoadsErrorHandler pins two page-level
// wirings: the way back names the page's profile instead of trusting the
// cookie another tab rewrites, and the script that shows failed requests is
// loaded (htmx alone drops every error response silently).
func TestLinksPageKeepsItsProfileAndLoadsErrorHandler(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	status, body := linksRequest(t, app, "GET", "/dashboard/links?profile=profile2")
	if status != 200 {
		t.Fatalf("links page status = %d: %s", status, body)
	}
	if !strings.Contains(body, `href="/dashboard?profile=profile2"`) {
		t.Fatalf("Back to dashboard does not carry the page's profile: %s", body)
	}
	if !strings.Contains(body, "/assets/links.js") {
		t.Fatalf("the links page does not load links.js: %s", body)
	}
}

// TestManualLinkAcceptsUnverifiableURL pins that the paste-a-URL fallback does
// not gate on the site answering: the sites most worth hand-linking are the
// ones this server cannot reach while the reader's browser can (MangaFire).
func TestManualLinkAcceptsUnverifiableURL(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		if err := registry.Register(unreachableConnector{key: "mangafire", hosts: []string{"mangafire.to"}}); err != nil {
			t.Fatalf("register unreachable connector: %v", err)
		}
	})
	defer cleanup()

	var comickID, mangadexID, mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick source: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangadex'`).Scan(&mangadexID); err != nil {
		t.Fatalf("look up mangadex source: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire source: %v", err)
	}

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'Series', ?, 'https://mangadex.org/title/abc', 'reading')
	`, mangadexID)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	trackerID, _ := result.LastInsertId()

	form := url.Values{"url": {"https://mangafire.to/title/l3z6m-kyou-kara-hajimeru-osananajimi"}}
	req := httptest.NewRequest("POST",
		"/dashboard/links/trackers/"+toString(int(trackerID))+"/manual?source="+toString(int(comickID)),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("manual link request: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("manual link status = %d: %s", res.StatusCode, body)
	}
	if strings.Contains(string(body), "Could not verify") {
		t.Fatalf("unreachable site must not block the manual link: %s", body)
	}

	var linkedURL string
	if err := db.QueryRow(`
		SELECT source_url FROM tracker_sources WHERE tracker_id = ? AND source_id = ?
	`, trackerID, mangafireID).Scan(&linkedURL); err != nil {
		t.Fatalf("expected the pasted URL to be linked: %v", err)
	}
	if linkedURL != "https://mangafire.to/title/l3z6m-kyou-kara-hajimeru-osananajimi" {
		t.Fatalf("linked url = %q", linkedURL)
	}
}

// linksPostForm posts a form as the named profile, the way the review page's
// requests carry it (hx-headers on the body).
func linksPostForm(t *testing.T, app *fiber.App, target string, form url.Values, profileKey string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Profile-Key", profileKey)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func linksSourceID(t *testing.T, db *sql.DB, key string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, key).Scan(&id); err != nil {
		t.Fatalf("look up %s source: %v", key, err)
	}
	return id
}

func linksSeedTracker(t *testing.T, db *sql.DB, profileID int64, title string, sourceID int64, sourceURL string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (?, ?, ?, ?, 'reading')
	`, profileID, title, sourceID, sourceURL)
	if err != nil {
		t.Fatalf("seed tracker %q: %v", title, err)
	}
	id, _ := result.LastInsertId()
	return id
}

// linksAssertUntouched checks that a refused manual link wrote nothing: no
// link row beyond the primary's mirror, no decision, no dismissal marker.
func linksAssertUntouched(t *testing.T, db *sql.DB, trackerID int64) {
	t.Helper()
	var links, markers int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, trackerID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM source_link_suggestions WHERE tracker_id = ? AND status != 'pending'
	`, trackerID).Scan(&markers); err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if links != 0 || markers != 0 {
		t.Fatalf("tracker %d gained %d link row(s) and %d decision row(s), want none", trackerID, links, markers)
	}
}

// TestManualLinkRefusesAPageThatIsNotASeries pins the series-URL check: a
// site's home page (or any page closer to the root than a series) used to
// answer 200, take the card away and link the home page as the series.
func TestManualLinkRefusesAPageThatIsNotASeries(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		if err := registry.Register(unreachableConnector{key: "mangadex", hosts: []string{"mangadex.org"}}); err != nil {
			t.Fatalf("register connector: %v", err)
		}
	})
	defer cleanup()

	comick := linksSourceID(t, db, "comick")
	trackerID := linksSeedTracker(t, db, 1, "Series", linksSourceID(t, db, "mangafire"), "https://mangafire.to/manga/series.abc")
	target := "/dashboard/links/trackers/" + toString(int(trackerID)) + "/manual?source=" + toString(int(comick))

	for pasted, want := range map[string]string{
		"https://mangadex.org/":       "home page",
		"https://www.mangadex.org":    "home page",
		"https://mangadex.org/titles": "not a series page on mangadex",
		"mangadex.org/title/abc":      "full address",
		"https://example.com/a/b":     "has no connector",
	} {
		status, body := linksPostForm(t, app, target, url.Values{"url": {pasted}}, "profile1")
		if status != fiber.StatusBadRequest {
			t.Fatalf("pasting %q: status = %d, want 400: %s", pasted, status, body)
		}
		if !strings.Contains(body, want) || strings.Contains(body, "<") {
			t.Fatalf("pasting %q answered %q, want a plain message mentioning %q", pasted, body, want)
		}
	}
	linksAssertUntouched(t, db, trackerID)
}

// TestManualLinkRefusesTheTrackersOwnSite: pasting the tracker's own address
// used to make the card vanish as if linked while nothing was linked, and the
// dismissal marker kept the tracker out of that source's scans for good.
func TestManualLinkRefusesTheTrackersOwnSite(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		if err := registry.Register(unreachableConnector{key: "mangafire", hosts: []string{"mangafire.to"}}); err != nil {
			t.Fatalf("register connector: %v", err)
		}
	})
	defer cleanup()

	comick := linksSourceID(t, db, "comick")
	primaryURL := "https://mangafire.to/title/l3z6m-osananajimi"
	trackerID := linksSeedTracker(t, db, 1, "Osananajimi", linksSourceID(t, db, "mangafire"), primaryURL)

	status, body := linksPostForm(t, app,
		"/dashboard/links/trackers/"+toString(int(trackerID))+"/manual?source="+toString(int(comick)),
		url.Values{"url": {primaryURL}}, "profile1")
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", status, body)
	}
	if !strings.Contains(body, "already this tracker's own site") {
		t.Fatalf("answer = %q, want it to say the URL is the tracker's own site", body)
	}
	linksAssertUntouched(t, db, trackerID)

	_, queue := linksRequest(t, app, "GET", "/dashboard/links/queue?source="+toString(int(comick)))
	if !strings.Contains(queue, "link-card-"+toString(int(trackerID))) {
		t.Fatalf("the tracker left the review queue: %s", queue)
	}
}

// TestCardActionsOnATrackerThatIsNotThereAnswer404: a manual link or a
// dismissal for a tracker deleted elsewhere, or another profile's, used to
// answer 200 with an empty card — the card vanished as if the action worked.
func TestCardActionsOnATrackerThatIsNotThereAnswer404(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		if err := registry.Register(unreachableConnector{key: "mangadex", hosts: []string{"mangadex.org"}}); err != nil {
			t.Fatalf("register connector: %v", err)
		}
	})
	defer cleanup()

	comick := linksSourceID(t, db, "comick")
	mangafire := linksSourceID(t, db, "mangafire")
	foreign := linksSeedTracker(t, db, 2, "Profile Two Series", mangafire, "https://mangafire.to/manga/two.abc")
	deleted := linksSeedTracker(t, db, 1, "Deleted Series", mangafire, "https://mangafire.to/manga/gone.abc")
	if _, err := db.Exec(`DELETE FROM trackers WHERE id = ?`, deleted); err != nil {
		t.Fatalf("delete tracker: %v", err)
	}

	for _, trackerID := range []int64{foreign, deleted} {
		base := "/dashboard/links/trackers/" + toString(int(trackerID))
		status, body := linksPostForm(t, app, base+"/manual?source="+toString(int(comick)),
			url.Values{"url": {"https://mangadex.org/title/0b8e1f3a-1111-2222-3333-444455556666"}}, "profile1")
		if status != fiber.StatusNotFound || !strings.Contains(body, "no longer in this queue") {
			t.Fatalf("manual link on tracker %d: %d %q, want 404 saying it is no longer in the queue", trackerID, status, body)
		}
		status, body = linksPostForm(t, app, base+"/dismiss?source="+toString(int(comick)), url.Values{}, "profile1")
		if status != fiber.StatusNotFound || !strings.Contains(body, "no longer in this queue") {
			t.Fatalf("dismiss on tracker %d: %d %q, want 404 saying it is no longer in the queue", trackerID, status, body)
		}
	}
	linksAssertUntouched(t, db, foreign)
}

// TestRejectingTheLastCandidateMovesTheRow pins where a tracker goes when its
// last candidate is rejected: its row is marked to move into the "no
// candidate" list (always rendered for it, even empty), in front of the next
// row in title order, and the summary counts it there.
func TestRejectingTheLastCandidateMovesTheRow(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	comick := linksSourceID(t, db, "comick")
	mangafire := linksSourceID(t, db, "mangafire")
	rejected := linksSeedTracker(t, db, 1, "Alpha", mangafire, "https://mangafire.to/manga/alpha.a")
	if _, err := db.Exec(`
		INSERT INTO source_link_suggestions (tracker_id, source_id, candidate_url, candidate_title, score, status)
		VALUES (?, ?, 'https://comick.io/comic/alpha', 'Alpha', 0.8, 'pending')
	`, rejected, comick); err != nil {
		t.Fatalf("seed suggestion: %v", err)
	}

	_, queue := linksRequest(t, app, "GET", "/dashboard/links/queue?source="+toString(int(comick)))
	if !strings.Contains(queue, `id="links-nomatch-list"`) {
		t.Fatalf("the empty no-candidate list is not rendered for a row to move into: %s", queue)
	}

	next := linksSeedTracker(t, db, 1, "Beta", mangafire, "https://mangafire.to/manga/beta.b")
	var suggestionID int64
	if err := db.QueryRow(`SELECT id FROM source_link_suggestions WHERE tracker_id = ?`, rejected).Scan(&suggestionID); err != nil {
		t.Fatalf("read suggestion id: %v", err)
	}
	status, body := linksPostForm(t, app, "/dashboard/links/suggestions/"+toString(int(suggestionID))+"/reject", url.Values{}, "profile1")
	if status != 200 {
		t.Fatalf("reject status = %d: %s", status, body)
	}
	row := `id="link-card-` + toString(int(rejected)) + `" data-link-move-before="` + toString(int(next)) + `"`
	if !strings.Contains(body, "link-card--nomatch") || !strings.Contains(body, row) {
		t.Fatalf("the rejected tracker's row is not marked to move in front of %d: %s", next, body)
	}
	if !strings.Contains(body, "<strong>0</strong> candidate(s) across <strong>0</strong> series") ||
		!strings.Contains(body, "<strong>2</strong> without a candidate") {
		t.Fatalf("the summary does not count the moved row: %s", body)
	}

	// The view showing only trackers with candidates has nowhere to put it.
	status, body = linksRequest(t, app, "GET", "/dashboard/links/queue?source="+toString(int(comick))+"&show=with")
	if status != 200 || strings.Contains(body, `id="links-nomatch-list"`) {
		t.Fatalf("the with-candidates view renders the no-candidate list: %s", body)
	}
}
