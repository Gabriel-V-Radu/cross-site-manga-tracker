package repository_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func countWhere(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func seedSaveTracker(t *testing.T, repo *repository.TrackerRepository) *models.Tracker {
	t.Helper()
	created, err := repo.Create(context.Background(), &models.Tracker{
		ProfileID: 1,
		Title:     "Original",
		SourceID:  1,
		SourceURL: "https://mangadex.org/title/original",
		Status:    "reading",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return created
}

// One save writes the row, the linked sources, the tags, the pasted link and
// the reading pin — and the primary is mirrored even when the submitted list
// left it out.
func TestSaveTrackerEditWritesEverythingTogether(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedSaveTracker(t, repo)

	tag, err := repo.CreateProfileTag(ctx, 1, "Favorite", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	edited := *created
	edited.Title = "Edited"
	pin := int64(2)
	saved, err := repo.SaveTrackerEdit(ctx, 1, created.ID, &edited, repository.TrackerLinks{
		Sources: []models.TrackerSource{
			{SourceID: 2, SourceURL: "https://mangafire.to/manga/original.abc"},
		},
		TagIDs:           []int64{tag.ID, 424242},
		ExtraSource:      &models.TrackerSource{SourceID: 3, SourceURL: "https://asurascans.com/comics/original"},
		SetReadingSource: true,
		ReadingSourceID:  &pin,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved == nil || saved.Title != "Edited" {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.ReadingSourceID == nil || *saved.ReadingSourceID != 2 {
		t.Fatalf("reading pin = %v, want 2", saved.ReadingSourceID)
	}
	if len(saved.Tags) != 1 || saved.Tags[0].ID != tag.ID {
		t.Fatalf("tags = %+v, want only the owned tag", saved.Tags)
	}

	// The submitted list (source 2), the pasted link (source 3) and the primary
	// mirror (source 1) are all linked.
	for _, sourceID := range []int64{1, 2, 3} {
		if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = ?`, created.ID, sourceID); got != 1 {
			t.Fatalf("source %d linked %d times, want 1", sourceID, got)
		}
	}
}

// A failure anywhere in the save leaves nothing behind. The pasted link names a
// source that does not exist, which the foreign key refuses after the row, the
// sources and the tags were already written inside the transaction.
func TestSaveTrackerEditRollsBackAsAWhole(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedSaveTracker(t, repo)
	tag, err := repo.CreateProfileTag(ctx, 1, "Favorite", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	edited := *created
	edited.Title = "Half Saved"
	_, err = repo.SaveTrackerEdit(ctx, 1, created.ID, &edited, repository.TrackerLinks{
		Sources:     []models.TrackerSource{{SourceID: 2, SourceURL: "https://mangafire.to/manga/x.abc"}},
		TagIDs:      []int64{tag.ID},
		ExtraSource: &models.TrackerSource{SourceID: 9999, SourceURL: "https://nowhere.example/x"},
	})
	if err == nil {
		t.Fatal("expected the unknown source to fail the save")
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM trackers WHERE id = ?`, created.ID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Original" {
		t.Fatalf("title = %q; the failed save wrote the row", title)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_tags WHERE tracker_id = ?`, created.ID); got != 0 {
		t.Fatalf("tags written by a failed save: %d", got)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = 2`, created.ID); got != 0 {
		t.Fatalf("sources written by a failed save: %d", got)
	}
}

// A tracker the profile does not own is neither found nor written.
func TestSaveTrackerEditRefusesAnotherProfilesTracker(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedSaveTracker(t, repo)

	edited := *created
	edited.Title = "Stolen"
	saved, err := repo.SaveTrackerEdit(ctx, 2, created.ID, &edited, repository.TrackerLinks{TagIDs: []int64{}})
	if err != nil || saved != nil {
		t.Fatalf("saved=%v err=%v, want nil, nil", saved, err)
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM trackers WHERE id = ?`, created.ID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Original" {
		t.Fatalf("title = %q; another profile's save wrote the row", title)
	}
}

// Update mirrors a changed primary source in the same transaction.
func TestUpdateMirrorsTheNewPrimaryAtomically(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedSaveTracker(t, repo)

	edited := *created
	edited.SourceID = 2
	edited.SourceURL = "https://mangafire.to/manga/original.abc"
	updated, err := repo.Update(ctx, 1, created.ID, &edited)
	if err != nil || updated == nil {
		t.Fatalf("update: %v %v", updated, err)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ? AND source_id = 2`, created.ID); got != 1 {
		t.Fatalf("new primary mirrored %d times, want 1", got)
	}
	if other, err := repo.Update(ctx, 2, created.ID, &edited); err != nil || other != nil {
		t.Fatalf("another profile's update: %v %v, want nil, nil", other, err)
	}
}
