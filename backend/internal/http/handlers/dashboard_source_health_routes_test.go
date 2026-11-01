package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

type healthPanelConnector struct {
	connectors.Site
}

func (c *healthPanelConnector) HealthCheck(context.Context) error { return nil }
func (c *healthPanelConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, nil
}
func (c *healthPanelConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

func getBody(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, path string) (int, string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), -1)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// The dashboard page itself must carry the indicator placeholder, or the panel
// is unreachable however correct its handlers are.
func TestDashboardPageLoadsTheSourceHealthIndicator(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	status, body := getBody(t, app, "/dashboard")
	if status != http.StatusOK || !strings.Contains(body, `hx-get="/dashboard/sources/indicator"`) {
		t.Fatalf("dashboard page has no source health indicator (status %d)", status)
	}
}

func TestSourceHealthIndicatorSaysPollingIsOffInsteadOfAllGood(t *testing.T) {
	_, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		_ = registry.Register(&healthPanelConnector{Site: connectors.Site{SiteKey: "asura", SiteName: "Asura"}})
	})
	defer cleanup()

	status, body := getBody(t, app, "/dashboard/sources/indicator")
	if status != http.StatusOK || !strings.Contains(body, "Polling off") || !strings.Contains(body, `hx-trigger="every 60s"`) {
		t.Fatalf("indicator = %d %q", status, body)
	}

	status, body = getBody(t, app, "/dashboard/sources")
	if status != http.StatusOK || !strings.Contains(body, "Polling is off in this instance") || !strings.Contains(body, "Asura") || !strings.Contains(body, "No data") {
		t.Fatalf("modal = %d %q", status, body)
	}
}

func TestSourceHealthPanelShowsAFailingSource(t *testing.T) {
	var board *connectors.SourceHealthBoard
	_, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		_ = registry.Register(&healthPanelConnector{Site: connectors.Site{SiteKey: "flame", SiteName: "FlameComics"}})
		board = registry.HealthBoard()
	})
	defer cleanup()

	board.PollerStarted(30 * time.Minute)
	blocked := &connectors.HTTPStatusError{StatusCode: 403, URL: "https://flamecomics.xyz/series/1"}
	for cycle := 0; cycle < 2; cycle++ {
		board.BeginCycle()
		board.RecordResolve("flame", connectors.RolePrimary, nil, blocked)
		board.RecordPrimaryFailure("flame", connectors.TrackedSeries{ID: 1, Title: "Solo Series", ProfileID: 1}, false)
		board.EndCycle()
	}

	status, body := getBody(t, app, "/dashboard/sources/indicator")
	if status != http.StatusOK || !strings.Contains(body, "source-health-pill--failing") || !strings.Contains(body, "1 source failing") {
		t.Fatalf("indicator = %d %q", status, body)
	}

	status, body = getBody(t, app, "/dashboard/sources?profile=profile1")
	for _, want := range []string{"FlameComics", "Failing", "blocked (HTTP 403)", "Solo Series", "unexpected status 403"} {
		if !strings.Contains(body, want) {
			t.Fatalf("modal (status %d) is missing %q:\n%s", status, want, body)
		}
	}

	// The other profile sees the same verdict but not this library's titles.
	_, body = getBody(t, app, "/dashboard/sources?profile=profile2")
	if strings.Contains(body, "Solo Series") || !strings.Contains(body, "1 more") {
		t.Fatalf("profile2 must see the count, not profile1's title:\n%s", body)
	}
}
