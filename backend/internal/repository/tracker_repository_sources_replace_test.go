package repository_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// linkRow is one tracker_sources row as the replace tests read it back. The
// timestamps are read as the text SQLite stores, so a row that was recreated
// shows as a changed created_at whatever the driver makes of the column type.
type linkRow struct {
	id           int64
	sourceURL    string
	itemID       sql.NullString
	inStep       int
	lastPolledAt string
	createdAt    string
	updatedAt    string
}

// linkRows returns the tracker's rows on one source, oldest first.
func linkRows(t *testing.T, db *sql.DB, trackerID, sourceID int64) []linkRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT id, source_url, source_item_id, in_step,
			COALESCE(CAST(last_polled_at AS TEXT), ''), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM tracker_sources
		WHERE tracker_id = ? AND source_id = ?
		ORDER BY id
	`, trackerID, sourceID)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	defer rows.Close()
	var out []linkRow
	for rows.Next() {
		var row linkRow
		if err := rows.Scan(&row.id, &row.sourceURL, &row.itemID, &row.inStep, &row.lastPolledAt, &row.createdAt, &row.updatedAt); err != nil {
			t.Fatalf("scan link: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate links: %v", err)
	}
	return out
}

func onlyLinkRow(t *testing.T, db *sql.DB, trackerID, sourceID int64) linkRow {
	t.Helper()
	rows := linkRows(t, db, trackerID, sourceID)
	if len(rows) != 1 {
		t.Fatalf("source %d linked %d times, want 1: %+v", sourceID, len(rows), rows)
	}
	return rows[0]
}

const (
	replaceMangafireURL = "https://mangafire.to/title/original"
	replaceComickURL    = "https://comick.io/comic/original"
	replaceMangahubURL  = "https://mangahub.io/manga/original"
	// What a poll that reached the site and found it in step leaves on a row,
	// on a day long before the test runs so a recreated row cannot match it.
	replacePolledAt  = "2026-11-07 09:00:00"
	replaceCreatedAt = "2026-10-01 08:00:00"
)

// seedPolledLinks creates a tracker (MangaDex primary, mirrored) linked to
// MangaFire and ComicK too, with every row carrying the rotation's memory of
// an answered, in-step poll.
func seedPolledLinks(t *testing.T, db *sql.DB, repo *repository.TrackerRepository) *models.Tracker {
	t.Helper()
	created := seedSaveTracker(t, repo)
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?), (?, ?, ?)
	`, created.ID, sourceIDByKey(t, db, "mangafire"), replaceMangafireURL,
		created.ID, sourceIDByKey(t, db, "comick"), replaceComickURL); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE tracker_sources
		SET in_step = 1, last_polled_at = ?, created_at = ?, updated_at = ?
		WHERE tracker_id = ?
	`, replacePolledAt, replaceCreatedAt, replaceCreatedAt, created.ID); err != nil {
		t.Fatalf("stamp links: %v", err)
	}
	return created
}

func assertRotationMemoryKept(t *testing.T, label string, before, after linkRow) {
	t.Helper()
	if after.id != before.id {
		t.Fatalf("%s: row %d was recreated as %d", label, before.id, after.id)
	}
	if after.inStep != 1 || after.lastPolledAt != before.lastPolledAt || after.lastPolledAt == "" {
		t.Fatalf("%s: rotation memory lost: in_step=%d last_polled_at=%q, want 1 and %q", label, after.inStep, after.lastPolledAt, before.lastPolledAt)
	}
	if after.createdAt != before.createdAt {
		t.Fatalf("%s: created_at = %q, want %q kept", label, after.createdAt, before.createdAt)
	}
}

// Replacing a tracker's links used to delete every row and insert the list,
// so the poll rotation forgot when it last asked each site and whether the
// site was in step — for the links that stayed as much as for the ones that
// went. A link in both the old set and the new keeps its row now.
func TestReplaceTrackerSourcesKeepsSurvivingLinksRotationMemory(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedPolledLinks(t, db, repo)
	mangafire, comick, mangahub := sourceIDByKey(t, db, "mangafire"), sourceIDByKey(t, db, "comick"), sourceIDByKey(t, db, "mangahub")

	primaryBefore := onlyLinkRow(t, db, created.ID, created.SourceID)
	mangafireBefore := onlyLinkRow(t, db, created.ID, mangafire)

	// MangaDex and MangaFire stay, ComicK goes, MangaHub is new. The MangaFire
	// entry is spelled differently and now knows the item id: it is still the
	// same link by the key the poller records its polls under.
	itemID := "original.abc"
	respelledMangafireURL := "  https://MangaFire.to/title/original "
	if err := repo.ReplaceTrackerSources(ctx, 1, created.ID, []models.TrackerSource{
		{SourceID: created.SourceID, SourceURL: created.SourceURL},
		{SourceID: mangafire, SourceURL: respelledMangafireURL, SourceItemID: &itemID},
		{SourceID: mangahub, SourceURL: replaceMangahubURL},
		{SourceID: 0, SourceURL: "https://nowhere.example/skipped"},
		{SourceID: mangahub, SourceURL: "   "},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	primaryAfter := onlyLinkRow(t, db, created.ID, created.SourceID)
	assertRotationMemoryKept(t, "untouched link", primaryBefore, primaryAfter)
	if primaryAfter.updatedAt != primaryBefore.updatedAt {
		t.Fatalf("a link the replace did not change was restamped: updated_at %q -> %q", primaryBefore.updatedAt, primaryAfter.updatedAt)
	}

	mangafireAfter := onlyLinkRow(t, db, created.ID, mangafire)
	assertRotationMemoryKept(t, "respelled link", mangafireBefore, mangafireAfter)
	if mangafireAfter.sourceURL != "https://MangaFire.to/title/original" {
		t.Fatalf("kept link url = %q, want the submitted spelling, trimmed", mangafireAfter.sourceURL)
	}
	if !mangafireAfter.itemID.Valid || mangafireAfter.itemID.String != itemID {
		t.Fatalf("kept link item id = %+v, want %q", mangafireAfter.itemID, itemID)
	}

	if dropped := linkRows(t, db, created.ID, comick); len(dropped) != 0 {
		t.Fatalf("the link left out of the list is still stored: %+v", dropped)
	}

	added := onlyLinkRow(t, db, created.ID, mangahub)
	if added.inStep != 0 || added.lastPolledAt != "" {
		t.Fatalf("a new link must start unpolled and not in step, got in_step=%d last_polled_at=%q", added.inStep, added.lastPolledAt)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, created.ID); got != 3 {
		t.Fatalf("tracker has %d links, want the three the list named", got)
	}
}

// The edit form sends the whole list on every save, so this is the path that
// did the damage: saving a title wiped the rotation's memory of every link.
// An entry without an item id must not blank the stored one either.
func TestSaveTrackerEditWithUnchangedLinksKeepsRotationMemory(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedPolledLinks(t, db, repo)
	mangafire, comick := sourceIDByKey(t, db, "mangafire"), sourceIDByKey(t, db, "comick")
	if _, err := db.Exec(`UPDATE tracker_sources SET source_item_id = 'known-id' WHERE tracker_id = ? AND source_id = ?`, created.ID, comick); err != nil {
		t.Fatalf("seed item id: %v", err)
	}

	before := map[int64]linkRow{}
	for _, sourceID := range []int64{created.SourceID, mangafire, comick} {
		before[sourceID] = onlyLinkRow(t, db, created.ID, sourceID)
	}

	edited := *created
	edited.Title = "Only The Title Changed"
	saved, err := repo.SaveTrackerEdit(ctx, 1, created.ID, &edited, repository.TrackerLinks{
		Sources: []models.TrackerSource{
			{SourceID: created.SourceID, SourceURL: created.SourceURL},
			{SourceID: mangafire, SourceURL: replaceMangafireURL},
			{SourceID: comick, SourceURL: replaceComickURL},
		},
	})
	if err != nil || saved == nil || saved.Title != "Only The Title Changed" {
		t.Fatalf("save: %+v %v", saved, err)
	}

	for sourceID, rowBefore := range before {
		rowAfter := onlyLinkRow(t, db, created.ID, sourceID)
		assertRotationMemoryKept(t, "link after a title edit", rowBefore, rowAfter)
		if rowAfter.itemID != rowBefore.itemID {
			t.Fatalf("source %d item id = %+v, want %+v kept", sourceID, rowAfter.itemID, rowBefore.itemID)
		}
	}
}

// Stored rows can differ only in the case of their URL. A list naming that
// link once leaves one row, as a replace always has, and renaming it to the
// submitted spelling must not collide with its twin on the unique key.
func TestReplaceTrackerSourcesFoldsStoredSpellingsOfOneLink(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedPolledLinks(t, db, repo)
	mangafire := sourceIDByKey(t, db, "mangafire")
	const twinURL = "https://mangafire.to/TITLE/original"
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?)`, created.ID, mangafire, twinURL); err != nil {
		t.Fatalf("seed twin link: %v", err)
	}
	polled := linkRows(t, db, created.ID, mangafire)[0]

	for _, submitted := range []string{twinURL, "https://mangafire.to/Title/Original", replaceMangafireURL} {
		if err := repo.ReplaceTrackerSources(ctx, 1, created.ID, []models.TrackerSource{
			{SourceID: mangafire, SourceURL: submitted},
		}); err != nil {
			t.Fatalf("replace with %q: %v", submitted, err)
		}
		row := onlyLinkRow(t, db, created.ID, mangafire)
		if row.sourceURL != submitted {
			t.Fatalf("link url = %q, want %q", row.sourceURL, submitted)
		}
	}

	// The first replace kept the twin, which was spelled as submitted; the
	// polled row went with it. From there on the one row is carried along.
	if row := onlyLinkRow(t, db, created.ID, mangafire); row.id == polled.id {
		t.Fatalf("expected the exactly-spelled twin to be the row kept, got the other (%d)", row.id)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, created.ID); got != 1 {
		t.Fatalf("tracker has %d links, want the one the list named", got)
	}
}

// An empty list unlinks everything, and another profile's replace touches
// nothing: it neither removes the owner's links nor adds its own.
func TestReplaceTrackerSourcesEmptyListAndForeignProfile(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedPolledLinks(t, db, repo)
	mangahub := sourceIDByKey(t, db, "mangahub")

	if err := repo.ReplaceTrackerSources(ctx, 2, created.ID, []models.TrackerSource{
		{SourceID: mangahub, SourceURL: replaceMangahubURL},
	}); err != nil {
		t.Fatalf("foreign replace: %v", err)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, created.ID); got != 3 {
		t.Fatalf("another profile's replace left %d links, want the owner's 3 untouched", got)
	}
	if got := len(linkRows(t, db, created.ID, mangahub)); got != 0 {
		t.Fatalf("another profile's replace linked %d rows to the tracker", got)
	}

	if err := repo.ReplaceTrackerSources(ctx, 1, created.ID, []models.TrackerSource{}); err != nil {
		t.Fatalf("empty replace: %v", err)
	}
	if got := countWhere(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE tracker_id = ?`, created.ID); got != 0 {
		t.Fatalf("an empty list left %d links", got)
	}
}
