package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
	"github.com/gofiber/fiber/v2"
)

type fakeConnector struct {
	key string
}

func (f *fakeConnector) Key() string                       { return f.key }
func (f *fakeConnector) Name() string                      { return "Fake " + f.key }
func (f *fakeConnector) Kind() string                      { return connectors.KindNative }
func (f *fakeConnector) HealthCheck(context.Context) error { return nil }
func (f *fakeConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, nil
}
func (f *fakeConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

// budgetConnector stands in for a check that must first wait for its slot in
// the shared per-host throttle: it fails when the context leaves it less time
// than that wait.
type budgetConnector struct {
	fakeConnector
	needs time.Duration
}

func (b *budgetConnector) HealthCheck(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < b.needs {
		return context.DeadlineExceeded
	}
	return nil
}

func setupAppForConnectors(t *testing.T) (*sql.DB, *fiber.App, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := database.ApplyMigrations(db); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations: %v", err)
	}

	registry := connectors.NewRegistry()
	_ = registry.Register(&fakeConnector{key: "mangadex"})
	_ = registry.Register(&fakeConnector{key: "mangafire"})

	app, err := apihttp.BuildServer(newTestConfig(t), db, registry)
	if err != nil {
		_ = db.Close()
		t.Fatalf("build server: %v", err)
	}
	cleanup := func() {
		_ = db.Close()
		_ = app.Shutdown()
	}

	return db, app, cleanup
}

func TestConnectorsEndpoints(t *testing.T) {
	_, app, cleanup := setupAppForConnectors(t)
	defer cleanup()

	listReq := httptest.NewRequest(http.MethodGet, "/v1/connectors", nil)
	listRes, err := app.Test(listReq)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRes.StatusCode)
	}

	var listPayload map[string]any
	if err := json.NewDecoder(listRes.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	items := listPayload["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("expected at least 2 connectors, got %d", len(items))
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/v1/connectors/health", nil)
	healthRes, err := app.Test(healthReq)
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	if healthRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", healthRes.StatusCode)
	}

	var healthPayload map[string]any
	if err := json.NewDecoder(healthRes.Body).Decode(&healthPayload); err != nil {
		t.Fatalf("decode health payload: %v", err)
	}
	healthItems := healthPayload["items"].([]any)
	if len(healthItems) < 2 {
		t.Fatalf("expected at least 2 health items, got %d", len(healthItems))
	}
}

// The endpoint's budget must outlast a queued throttle slot. api.comick.dev is
// spaced 3.5s apart, so while the poller's ComicK shard runs a probe can wait
// that long, and more behind a link scan; the old 3s budget reported a healthy
// ComicK as failing. Ten seconds is what two queued slots on the slowest host
// plus the request itself can take.
func TestConnectorsHealthLeavesRoomForAQueuedThrottleSlot(t *testing.T) {
	_, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		_ = registry.Register(&budgetConnector{fakeConnector: fakeConnector{key: "comick"}, needs: 10 * time.Second})
	})
	defer cleanup()

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/connectors/health", nil))
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var payload struct {
		Items []connectors.HealthStatus `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode health payload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 health item, got %d", len(payload.Items))
	}
	if !payload.Items[0].Healthy {
		t.Fatalf("a check that only needs to wait for its slot must report healthy, got error %q", payload.Items[0].Error)
	}
}
