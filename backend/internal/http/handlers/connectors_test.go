package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
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

func setupAppForConnectors(t *testing.T) (*sql.DB, *fiber.App, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	_, currentFile, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(currentFile)
	migrationsPath := filepath.Join(baseDir, "..", "..", "..", "migrations")
	if err := database.ApplyMigrations(db, migrationsPath); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations: %v", err)
	}

	registry := connectors.NewRegistry()
	_ = registry.Register(&fakeConnector{key: "mangadex"})
	_ = registry.Register(&fakeConnector{key: "mangafire"})

	app := apihttp.NewServerWithRegistry(config.Config{AppName: "test"}, db, registry)
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
