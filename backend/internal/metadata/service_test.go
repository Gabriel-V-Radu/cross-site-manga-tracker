package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func openServiceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.ApplyMigrations(db); err != nil {
		t.Fatal(err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func sourceIDOf(t *testing.T, db *sql.DB, key string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, key).Scan(&id); err != nil {
		t.Fatalf("source %s: %v", key, err)
	}
	return id
}

func TestServiceRecordsAtMostWeeklyAndReconciles(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	repo := repository.NewMetadataRepository(db)
	service := NewService(repo, nil, Config{}, nil)
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }

	mangafire := sourceIDOf(t, db, "mangafire")
	mangadex := sourceIDOf(t, db, "mangadex")
	result, err := db.Exec(`INSERT INTO trackers (profile_id, title, source_id, source_url, status) VALUES (1, 'Series', ?, 'https://mangafire.to/title/abc.series', 'reading')`, mangafire)
	if err != nil {
		t.Fatal(err)
	}
	trackerID, _ := result.LastInsertId()
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, 'https://mangadex.org/title/0b1d2c3e-0000-0000-0000-000000000000')`, trackerID, mangadex); err != nil {
		t.Fatal(err)
	}

	service.Record(ctx, mangafire, "https://mangafire.to/title/abc.series", &connectors.SeriesMetadata{Type: "manhwa", Genres: []string{"Action"}, Status: "ongoing"})
	if !service.dirty.Load() {
		t.Fatal("a first record must mark the views dirty")
	}
	written, err := service.Reconcile(ctx)
	if err != nil || written != 1 {
		t.Fatalf("Reconcile = %d, %v; want one view written", written, err)
	}

	row, err := repo.GetTrackerMetadata(ctx, 1, trackerID)
	if err != nil || row == nil {
		t.Fatalf("view = %v, %v", row, err)
	}
	if row.SeriesType != "manhwa" || row.SeriesStatus != "ongoing" || row.GenresJSON != `["Action"]` {
		t.Fatalf("view = %+v", row)
	}

	// A day later the same site says something new: too soon, dropped.
	clock = clock.Add(24 * time.Hour)
	service.Record(ctx, mangafire, "https://mangafire.to/title/abc.series", &connectors.SeriesMetadata{Type: "manhwa", Status: "completed"})
	if written, _ := service.Reconcile(ctx); written != 0 {
		t.Fatalf("a record inside the refresh window must not change anything, rebuilt %d", written)
	}

	// Another site's first record is stored at once and joins the view.
	service.Record(ctx, mangadex, "https://mangadex.org/title/0b1d2c3e-0000-0000-0000-000000000000", &connectors.SeriesMetadata{Genres: []string{"Romance"}})
	if written, _ := service.Reconcile(ctx); written != 1 {
		t.Fatalf("a new site's metadata must rebuild the view, rebuilt %d", written)
	}
	row, _ = repo.GetTrackerMetadata(ctx, 1, trackerID)
	var view Consolidated
	if err := json.Unmarshal([]byte(row.Payload), &view); err != nil {
		t.Fatal(err)
	}
	if !contains(view.Genres, "Action") || !contains(view.Genres, "Romance") || view.Status != "ongoing" {
		t.Fatalf("view = %+v", view)
	}

	// Past the window the new status is taken.
	clock = clock.Add(RefreshAfter)
	service.Record(ctx, mangafire, "https://mangafire.to/title/abc.series", &connectors.SeriesMetadata{Type: "manhwa", Status: "completed"})
	if written, _ := service.Reconcile(ctx); written != 1 {
		t.Fatalf("a refreshed row must rebuild the view, rebuilt %d", written)
	}
	row, _ = repo.GetTrackerMetadata(ctx, 1, trackerID)
	if row.SeriesStatus != "completed" {
		t.Fatalf("status = %q, want the refreshed completed", row.SeriesStatus)
	}

	// Unlinking a site rebuilds the view without it.
	if _, err := db.Exec(`DELETE FROM tracker_sources WHERE source_id = ?`, mangadex); err != nil {
		t.Fatal(err)
	}
	if written, _ := service.Reconcile(ctx); written != 1 {
		t.Fatalf("an unlinked site must rebuild the view, rebuilt %d", written)
	}
	row, _ = repo.GetTrackerMetadata(ctx, 1, trackerID)
	if row.GenresJSON != `[]` {
		t.Fatalf("genres after unlinking = %s", row.GenresJSON)
	}
}

func TestServiceRecordIgnoresEmptyMetadata(t *testing.T) {
	db := openServiceTestDB(t)
	service := NewService(repository.NewMetadataRepository(db), nil, Config{}, nil)
	service.Record(context.Background(), 1, "https://mangadex.org/title/x", &connectors.SeriesMetadata{})
	service.Record(context.Background(), 1, "https://mangadex.org/title/x", nil)
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM source_metadata`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows = %d, %v", count, err)
	}
}
