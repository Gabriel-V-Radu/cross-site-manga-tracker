package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/linkscan"
	"github.com/gofiber/fiber/v2"
)

// blockingScanConnector parks every search until its context is cancelled,
// so a scan started against it stays running for as long as a test needs.
type blockingScanConnector struct {
	key     string
	entered chan struct{}
	once    *sync.Once
}

func (c blockingScanConnector) Key() string                       { return c.key }
func (c blockingScanConnector) Name() string                      { return c.key }
func (c blockingScanConnector) Kind() string                      { return connectors.KindNative }
func (c blockingScanConnector) HealthCheck(context.Context) error { return nil }
func (c blockingScanConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, errors.New("not used")
}
func (c blockingScanConnector) SearchByTitle(ctx context.Context, _ string, _ int) ([]connectors.MangaResult, error) {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// newLinkScanTestHandler builds a real handler whose scanner searches a
// blocking "comick" and asks no metadata aggregator — the production scanner
// would query MangaBaka over the network for every tracker. Only the Link
// Review routes are mounted.
func newLinkScanTestHandler(t *testing.T) (*DashboardHandler, *fiber.App, blockingScanConnector, int64) {
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

	connector := blockingScanConnector{key: "comick", entered: make(chan struct{}), once: &sync.Once{}}
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	_, currentFile, _, _ := runtime.Caller(0)
	webDir := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "web")
	handler := NewDashboardHandler(db, registry, DashboardPaths{
		TemplatesGlob: filepath.Join(webDir, "templates", "*.html"),
		AssetsDir:     filepath.Join(webDir, "assets"),
	})
	if err := handler.TemplateError(); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	handler.linkScanner.Close()
	handler.linkScanner = linkscan.NewScanner(handler.linkSuggestionRepo, registry, nil, nil)
	t.Cleanup(handler.Close)

	var comickID, mangafireID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'comick'`).Scan(&comickID); err != nil {
		t.Fatalf("look up comick: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = 'mangafire'`).Scan(&mangafireID); err != nil {
		t.Fatalf("look up mangafire: %v", err)
	}
	for _, profileID := range []int64{1, 2} {
		if _, err := db.Exec(`
			INSERT INTO trackers (profile_id, title, source_id, source_url, status)
			VALUES (?, 'Unlinked', ?, 'https://mangafire.to/manga/unlinked.a', 'reading')
		`, profileID, mangafireID); err != nil {
			t.Fatalf("seed tracker: %v", err)
		}
	}

	app := fiber.New()
	app.Get("/dashboard/links", handler.LinksPage)
	app.Post("/dashboard/links/scan", handler.StartLinkScan)
	app.Post("/dashboard/links/scan/stop", handler.StopLinkScan)
	app.Get("/dashboard/links/scan-status", handler.LinkScanStatus)
	return handler, app, connector, comickID
}

func linkScanRequest(t *testing.T, app *fiber.App, method string, target string, profileKey string) string {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("X-Profile-Key", profileKey)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s as %s: status %d: %s", method, target, profileKey, res.StatusCode, body)
	}
	return string(body)
}

// TestLinkScanIsTheStartingProfilesOwn pins the scan's per-profile view at the
// handler: the other profile's page used to show the running scan with a Stop
// that ended it, refuse its own Scan with "A scan is still running", and then
// report the finished scan as its own last one.
func TestLinkScanIsTheStartingProfilesOwn(t *testing.T) {
	handler, app, connector, comickID := newLinkScanTestHandler(t)
	scanURL := "/dashboard/links/scan?source=" + strconv.FormatInt(comickID, 10)

	started := linkScanRequest(t, app, "POST", scanURL, "profile1")
	if !strings.Contains(started, "link-scan-status__stop") {
		t.Fatalf("the starting profile gets no Stop: %s", started)
	}
	<-connector.entered

	other := linkScanRequest(t, app, "GET", "/dashboard/links/scan-status", "profile2")
	if !strings.Contains(other, "Another profile is running a scan") {
		t.Fatalf("the other profile is not told another profile's scan is running: %s", other)
	}
	if strings.Contains(other, "Stop") || strings.Contains(other, "Scanning") || strings.Contains(other, `"poll"`) {
		t.Fatalf("the other profile sees the scan as its own: %s", other)
	}

	linkScanRequest(t, app, "POST", "/dashboard/links/scan/stop", "profile2")
	if own := linkScanRequest(t, app, "GET", "/dashboard/links/scan-status?poll=1", "profile1"); !strings.Contains(own, "Scanning ComicK") || strings.Contains(own, "Stopping") {
		t.Fatalf("another profile's Stop reached the scan: %s", own)
	}

	refused := linkScanRequest(t, app, "POST", scanURL, "profile2")
	if !strings.Contains(refused, "Another profile&#39;s scan is running") {
		t.Fatalf("the other profile's Scan is not told why it did not start: %s", refused)
	}

	stopping := linkScanRequest(t, app, "POST", "/dashboard/links/scan/stop", "profile1")
	if !strings.Contains(stopping, "Stopping after the current series") {
		t.Fatalf("the owner's Stop did not reach the scan: %s", stopping)
	}

	handler.linkScanner.Close()
	if own := linkScanRequest(t, app, "GET", "/dashboard/links/scan-status", "profile1"); !strings.Contains(own, "Last scan (ComicK)") || !strings.Contains(own, "stopped early") {
		t.Fatalf("the owner does not see its finished scan: %s", own)
	}
	if other := linkScanRequest(t, app, "GET", "/dashboard/links/scan-status", "profile2"); strings.Contains(other, "Last scan") || strings.Contains(other, "Another profile") {
		t.Fatalf("the other profile shows a scan it never ran: %s", other)
	}
}

// TestLinksPagePresetsOnlyItsOwnProfilesRecentScanScope pins the scope the
// review page opens on: one profile's scan scope used to preselect every
// profile's page, and every reload, with nothing saying so.
func TestLinksPagePresetsOnlyItsOwnProfilesRecentScanScope(t *testing.T) {
	_, app, connector, comickID := newLinkScanTestHandler(t)

	linkScanRequest(t, app, "POST", "/dashboard/links/scan?source="+strconv.FormatInt(comickID, 10)+"&status=reading&primary=any&alternates=0", "profile1")
	<-connector.entered

	own := linkScanRequest(t, app, "GET", "/dashboard/links?profile=profile1", "profile1")
	if !strings.Contains(own, `value="reading" selected`) || !strings.Contains(own, `value="0" selected`) {
		t.Fatalf("the scanning profile's page does not open on its scan's scope: %s", own)
	}
	if !strings.Contains(own, "data-scope-hint") {
		t.Fatalf("the preset scope is not announced: %s", own)
	}

	other := linkScanRequest(t, app, "GET", "/dashboard/links?profile=profile2", "profile2")
	if !strings.Contains(other, `value="all" selected`) || strings.Contains(other, `value="reading" selected`) || strings.Contains(other, "data-scope-hint") {
		t.Fatalf("another profile's page opens on the scan's scope: %s", other)
	}

	// The page URL's own scope (links.js writes every change into it) wins,
	// so a reload shows what the reader chose since.
	chosen := linkScanRequest(t, app, "GET", "/dashboard/links?profile=profile1&source="+strconv.FormatInt(comickID, 10)+"&status=completed&primary=any&alternates=any&show=without", "profile1")
	if !strings.Contains(chosen, `value="completed" selected`) || !strings.Contains(chosen, `value="without" selected`) || strings.Contains(chosen, "data-scope-hint") {
		t.Fatalf("a reload with its own scope reverted to the scan's: %s", chosen)
	}
}

// TestLinkScanStatusRendersAStartingScan pins the starting state's line: the
// total is not known yet, and "0/0 series checked" read as nothing to do.
func TestLinkScanStatusRendersAStartingScan(t *testing.T) {
	handler, _, _, _ := newLinkScanTestHandler(t)

	var out bytes.Buffer
	if err := handler.templates.ExecuteTemplate(&out, "links_scan_status_partial.html", linkScanStatusData{
		Running: true, Starting: true, SourceName: "MangaHub",
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "Starting the MangaHub scan") || strings.Contains(body, "0/0") {
		t.Fatalf("a starting scan renders %s", body)
	}
	if !strings.Contains(body, "link-scan-status__stop") {
		t.Fatalf("a starting scan cannot be stopped: %s", body)
	}
}
