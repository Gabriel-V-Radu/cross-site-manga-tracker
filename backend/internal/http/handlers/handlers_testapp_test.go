package handlers_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
	"github.com/gofiber/fiber/v2"
)

// testEnv is one built app and everything a test needs to look behind it. The
// config is exposed because the paths the handlers write to now come from it:
// a test that checks a file landed on disk asks the config where, instead of
// assuming the process's working directory.
type testEnv struct {
	db      *sql.DB
	app     *fiber.App
	cfg     config.Config
	cleanup func()
}

func setupTestApp(t *testing.T) (*sql.DB, *fiber.App, func()) {
	t.Helper()
	return setupTestAppWithRegistry(t, nil)
}

// setupTestAppWithRegistry builds the app around a custom connector registry,
// for tests that need a connector with a known key to behave a chosen way
// (e.g. an unreachable "mangafire") without touching the network.
func setupTestAppWithRegistry(t *testing.T, configure func(*connectors.Registry)) (*sql.DB, *fiber.App, func()) {
	t.Helper()
	env := newTestEnv(t, configure)
	return env.db, env.app, env.cleanup
}

// newTestEnv builds the app the handler tests run against. It used to chdir to
// the backend root, because the templates, the asset stamps and the upload
// directory were all resolved against the working directory; they come from
// the config now, so the templates are read from the repository's own web/
// and everything written goes to the test's temp directory.
func newTestEnv(t *testing.T, configure func(*connectors.Registry)) testEnv {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := database.ApplyMigrations(db, filepath.Join(backendRootDir(), "migrations")); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		_ = db.Close()
		t.Fatalf("seed defaults: %v", err)
	}

	cfg := newTestConfig(t)

	var app *fiber.App
	if configure != nil {
		registry := connectors.NewRegistry()
		configure(registry)
		app = apihttp.NewServerWithRegistry(cfg, db, registry)
	} else {
		app = apihttp.NewServer(cfg, db)
	}

	return testEnv{
		db:  db,
		app: app,
		cfg: cfg,
		cleanup: func() {
			_ = app.Shutdown()
			_ = db.Close()
			_ = os.RemoveAll(tmpDir)
		},
	}
}

// newTestConfig points the handlers at the repository's real templates and at
// a throwaway data directory. Every test app needs both: without them the
// paths fall back to whatever "" resolves to, which means the package
// directory — tests that upload a logo or cache a cover would write into the
// source tree.
func newTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		AppName: "test-app",
		WebDir:  filepath.Join(backendRootDir(), "web"),
		DataDir: filepath.Join(t.TempDir(), "data"),
	}
}

func backendRootDir() string {
	_, currentFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
}

func toString(value int) string {
	return strconv.Itoa(value)
}
