package handlers_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
	"github.com/gofiber/fiber/v2"
)

func setupTestApp(t *testing.T) (*sql.DB, *fiber.App, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	_, currentFile, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(currentFile)
	backendRoot := filepath.Clean(filepath.Join(baseDir, "..", "..", ".."))
	originalWD, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(backendRoot); err != nil {
		_ = db.Close()
		t.Fatalf("set working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	migrationsPath := filepath.Join(baseDir, "..", "..", "..", "migrations")
	if err := database.ApplyMigrations(db, migrationsPath); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		_ = db.Close()
		t.Fatalf("seed defaults: %v", err)
	}

	cfg := config.Config{AppName: "test-app"}
	app := apihttp.NewServer(cfg, db)

	cleanup := func() {
		_ = app.Shutdown()
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return db, app, cleanup
}

func toString(value int) string {
	return strconv.Itoa(value)
}
