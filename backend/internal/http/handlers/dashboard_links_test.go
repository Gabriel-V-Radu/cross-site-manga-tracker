package handlers_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
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
