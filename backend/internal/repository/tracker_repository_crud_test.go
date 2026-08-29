package repository_test

import (
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// TestCreateWritesTrackerAndPrimarySourceTogether pins Create's atomicity: the
// tracker row and its primary tracker_sources mirror are one transaction, so a
// successful Create always yields both.
func TestCreateWritesTrackerAndPrimarySourceTogether(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)

	itemID := "series-item"
	created, err := repo.Create(&models.Tracker{
		ProfileID:    1,
		Title:        "Created Tracker",
		SourceID:     1,
		SourceItemID: &itemID,
		SourceURL:    "https://mangadex.org/title/created",
		Status:       "reading",
	})
	if err != nil {
		t.Fatalf("create tracker: %v", err)
	}
	if created == nil || created.ID == 0 {
		t.Fatalf("expected the created tracker back, got %#v", created)
	}

	var sources int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM tracker_sources
		WHERE tracker_id = ? AND source_id = 1 AND source_url = ?
	`, created.ID, "https://mangadex.org/title/created").Scan(&sources); err != nil {
		t.Fatalf("count tracker sources: %v", err)
	}
	if sources != 1 {
		t.Fatalf("expected the primary source row to be committed with the tracker, got %d", sources)
	}
}

// TestCreateLeavesNothingBehindOnFailure covers the rollback half: when any
// statement in the transaction fails (here the tracker INSERT, via a source id
// that violates the foreign key), no partial rows survive.
func TestCreateLeavesNothingBehindOnFailure(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)

	if _, err := repo.Create(&models.Tracker{
		ProfileID: 1,
		Title:     "Doomed Tracker",
		SourceID:  9999,
		SourceURL: "https://example.org/series",
		Status:    "reading",
	}); err == nil {
		t.Fatalf("expected create with a nonexistent source to fail")
	}

	var trackers int
	if err := db.QueryRow(`SELECT COUNT(1) FROM trackers WHERE title = ?`, "Doomed Tracker").Scan(&trackers); err != nil {
		t.Fatalf("count trackers: %v", err)
	}
	if trackers != 0 {
		t.Fatalf("expected the failed create to leave no tracker row, got %d", trackers)
	}
}
