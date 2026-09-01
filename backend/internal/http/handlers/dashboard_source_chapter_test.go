package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
	"github.com/gofiber/fiber/v2"
)

// chapterLookupConnector stands in for a source whose search listing carries no
// chapter number, so the number only exists behind ResolveByURL.
type chapterLookupConnector struct {
	key       string
	result    *connectors.MangaResult
	err       error
	resolved  chan string
	resolveNo int
}

func (c *chapterLookupConnector) Key() string                       { return c.key }
func (c *chapterLookupConnector) Name() string                      { return "MangaFire" }
func (c *chapterLookupConnector) Kind() string                      { return connectors.KindNative }
func (c *chapterLookupConnector) HealthCheck(context.Context) error { return nil }
func (c *chapterLookupConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (c *chapterLookupConnector) ResolveByURL(_ context.Context, rawURL string) (*connectors.MangaResult, error) {
	c.resolveNo++
	select {
	case c.resolved <- rawURL:
	default:
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.result, nil
}

func setupAppForChapterLookup(t *testing.T, connector connectors.Connector) (*fiber.App, int64, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	_, currentFile, _, _ := runtime.Caller(0)
	migrationsPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "migrations")
	if err := database.ApplyMigrations(db, migrationsPath); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations: %v", err)
	}

	// Inserted rather than seeded so the test does not depend on the default
	// source list, only on a source the stub connector answers for.
	inserted, err := db.Exec(
		`INSERT INTO sources (key, name, connector_kind, base_url, enabled) VALUES (?, ?, ?, ?, 1)`,
		connector.Key(), connector.Name(), connector.Kind(), "https://mangafire.to")
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert %s source: %v", connector.Key(), err)
	}
	sourceID, err := inserted.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read inserted source id: %v", err)
	}

	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		_ = db.Close()
		t.Fatalf("register connector: %v", err)
	}

	app, err := apihttp.BuildServer(newTestConfig(t), db, registry)
	if err != nil {
		_ = db.Close()
		t.Fatalf("build server: %v", err)
	}
	return app, sourceID, func() {
		_ = db.Close()
		_ = app.Shutdown()
	}
}

func requestSourceChapter(t *testing.T, app *fiber.App, query string) (int, map[string]string) {
	t.Helper()

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/dashboard/trackers/source-chapter"+query, nil), 5000)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	payload := map[string]string{}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return res.StatusCode, payload
}

// The whole point of the endpoint: the tracker form gets the number while the
// user is still filling it in, instead of waiting for the first poll after save.
func TestResolveSourceChapterReturnsLatestChapterForForm(t *testing.T) {
	latest := 54.0
	releaseAt := time.Date(2026, 8, 8, 17, 30, 17, 0, time.UTC)
	connector := &chapterLookupConnector{
		key:      "mangafire",
		resolved: make(chan string, 1),
		result: &connectors.MangaResult{
			SourceKey:     "mangafire",
			SourceItemID:  "mv3r7-reiwa-no-dara-san",
			Title:         "Reiwa no Dara-san",
			LatestChapter: &latest,
			LastUpdatedAt: &releaseAt,
		},
	}

	app, sourceID, cleanup := setupAppForChapterLookup(t, connector)
	defer cleanup()

	sourceURL := "https://mangafire.to/title/mv3r7-reiwa-no-dara-san"
	status, payload := requestSourceChapter(t, app,
		"?source_id="+strconv.FormatInt(sourceID, 10)+"&url="+sourceURL)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if payload["latestChapter"] != "54" {
		t.Fatalf("expected latest chapter 54, got %q", payload["latestChapter"])
	}
	if payload["latestReleaseAt"] != releaseAt.Format(time.RFC3339) {
		t.Fatalf("expected release date %s, got %q", releaseAt.Format(time.RFC3339), payload["latestReleaseAt"])
	}

	select {
	case got := <-connector.resolved:
		if got != sourceURL {
			t.Fatalf("expected the picked result's URL to be resolved, got %q", got)
		}
	default:
		t.Fatalf("expected the connector to be asked to resolve")
	}
}

// A title the source carries in no English edition has no number to give. That
// is an answer, not a failure: the field stays empty and the form stays usable.
func TestResolveSourceChapterReportsAbsentChapterAsEmpty(t *testing.T) {
	connector := &chapterLookupConnector{
		key:      "mangafire",
		resolved: make(chan string, 1),
		result: &connectors.MangaResult{
			SourceKey:    "mangafire",
			SourceItemID: "jpn-raws-only",
			Title:        "Raws Only",
		},
	}

	app, sourceID, cleanup := setupAppForChapterLookup(t, connector)
	defer cleanup()

	status, payload := requestSourceChapter(t, app,
		"?source_id="+strconv.FormatInt(sourceID, 10)+"&url=https://mangafire.to/title/jpn-raws-only")

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if payload["latestChapter"] != "" {
		t.Fatalf("expected no chapter number, got %q", payload["latestChapter"])
	}
	if payload["error"] != "" {
		t.Fatalf("an absent chapter is not an error, got %q", payload["error"])
	}
}

// A source that cannot be read must say so rather than answer "no chapters",
// which the form would take at face value and leave blank without explanation.
func TestResolveSourceChapterReportsResolveFailure(t *testing.T) {
	connector := &chapterLookupConnector{
		key:      "mangafire",
		resolved: make(chan string, 1),
		err:      context.DeadlineExceeded,
	}

	app, sourceID, cleanup := setupAppForChapterLookup(t, connector)
	defer cleanup()

	status, payload := requestSourceChapter(t, app,
		"?source_id="+strconv.FormatInt(sourceID, 10)+"&url=https://mangafire.to/title/dkw-one-piece")

	if status != http.StatusOK {
		t.Fatalf("expected a 200 carrying the error, got %d", status)
	}
	if payload["error"] == "" {
		t.Fatalf("expected an error message when the source cannot be read")
	}
	if payload["latestChapter"] != "" {
		t.Fatalf("expected no chapter number on failure, got %q", payload["latestChapter"])
	}
}

func TestResolveSourceChapterRejectsIncompleteRequests(t *testing.T) {
	connector := &chapterLookupConnector{key: "mangafire", resolved: make(chan string, 1)}
	app, sourceID, cleanup := setupAppForChapterLookup(t, connector)
	defer cleanup()

	cases := []struct {
		name  string
		query string
	}{
		{"missing url", "?source_id=" + strconv.FormatInt(sourceID, 10)},
		{"missing source", "?url=https://mangafire.to/title/dkw-one-piece"},
		{"unknown source", "?source_id=999999&url=https://mangafire.to/title/dkw-one-piece"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, _ := requestSourceChapter(t, app, testCase.query)
			if status == http.StatusOK {
				t.Fatalf("expected a rejection, got 200")
			}
		})
	}

	if connector.resolveNo != 0 {
		t.Fatalf("expected no connector call for an incomplete request, got %d", connector.resolveNo)
	}
}
