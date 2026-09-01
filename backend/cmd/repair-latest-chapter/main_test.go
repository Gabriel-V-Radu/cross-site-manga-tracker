package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.ApplyMigrations(db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	return db
}

func seedPoisonedTracker(t *testing.T, db *sql.DB, chapter float64, lastRead *float64) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (
			profile_id, title, source_id, source_url, status,
			latest_known_chapter, last_read_chapter,
			latest_chapter_seen_at, pending_lower_chapter, pending_lower_first_seen_at
		)
		VALUES (1, 'Poisoned', (SELECT id FROM sources WHERE key = 'mangafire'),
			'https://mangafire.to/manga/poisoned', 'reading',
			?, ?, CURRENT_TIMESTAMP, 230, CURRENT_TIMESTAMP)
	`, chapter, lastRead)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seeded tracker id: %v", err)
	}
	return id
}

func TestParseAssignments(t *testing.T) {
	assignments, err := parseAssignments(" 167=199 , 28=271.5 ")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(assignments) != 2 || assignments[0].TrackerID != 28 || assignments[0].Chapter != 271.5 ||
		assignments[1].TrackerID != 167 || assignments[1].Chapter != 199 {
		t.Fatalf("unexpected assignments: %+v", assignments)
	}

	for _, bad := range []string{"28", "x=1", "28=abc", "28=1,28=2", "0=5", "28=-1"} {
		if _, err := parseAssignments(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestRunRepairsSetsChapterAndClearsPendingState(t *testing.T) {
	db := openTestDB(t)
	id := seedPoisonedTracker(t, db, 10000, nil)

	repaired, err := runRepairs(db, []assignment{{TrackerID: id, Chapter: 62}}, false)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("expected 1 repair, got %d", repaired)
	}

	var (
		chapter       sql.NullFloat64
		seenAt        sql.NullString
		createdAt     string
		reporter      sql.NullInt64
		pending       sql.NullFloat64
		pendingSeenAt sql.NullString
	)
	if err := db.QueryRow(`
		SELECT latest_known_chapter, latest_chapter_seen_at, created_at, latest_chapter_source_id,
			pending_lower_chapter, pending_lower_first_seen_at
		FROM trackers WHERE id = ?
	`, id).Scan(&chapter, &seenAt, &createdAt, &reporter, &pending, &pendingSeenAt); err != nil {
		t.Fatalf("read tracker: %v", err)
	}
	if !chapter.Valid || chapter.Float64 != 62 {
		t.Fatalf("expected chapter 62, got %#v", chapter)
	}
	if pending.Valid || pendingSeenAt.Valid {
		t.Fatalf("expected pending state cleared, got pending=%#v pendingSeenAt=%#v", pending, pendingSeenAt)
	}
	// Not NULL: the next poll would fill a NULL with its own time and rank the
	// repaired tracker as freshly updated. With no release date on record the
	// fallback is the tracker's creation, the rule migration 0018 used.
	if !seenAt.Valid || seenAt.String != createdAt {
		t.Fatalf("expected latest_chapter_seen_at to fall back to created_at %q, got %#v", createdAt, seenAt)
	}
	if reporter.Valid {
		t.Fatalf("expected the reporting source to be cleared, got %d", reporter.Int64)
	}
}

func TestRunRepairsClampsToReadPosition(t *testing.T) {
	db := openTestDB(t)
	lastRead := 80.0
	id := seedPoisonedTracker(t, db, 9999, &lastRead)

	if _, err := runRepairs(db, []assignment{{TrackerID: id, Chapter: 62}}, false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	var chapter sql.NullFloat64
	if err := db.QueryRow(`SELECT latest_known_chapter FROM trackers WHERE id = ?`, id).Scan(&chapter); err != nil {
		t.Fatalf("read tracker: %v", err)
	}
	if !chapter.Valid || chapter.Float64 != lastRead {
		t.Fatalf("expected clamp to read position %.0f, got %#v", lastRead, chapter)
	}
}

func TestRunRepairsDryRunWritesNothing(t *testing.T) {
	db := openTestDB(t)
	id := seedPoisonedTracker(t, db, 10000, nil)

	if _, err := runRepairs(db, []assignment{{TrackerID: id, Chapter: 62}}, true); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}

	var chapter sql.NullFloat64
	if err := db.QueryRow(`SELECT latest_known_chapter FROM trackers WHERE id = ?`, id).Scan(&chapter); err != nil {
		t.Fatalf("read tracker: %v", err)
	}
	if !chapter.Valid || chapter.Float64 != 10000 {
		t.Fatalf("dry run must not write, got %#v", chapter)
	}
}

func TestRunRepairsRejectsUnknownTracker(t *testing.T) {
	db := openTestDB(t)
	if _, err := runRepairs(db, []assignment{{TrackerID: 424242, Chapter: 10}}, false); err == nil {
		t.Fatalf("expected an error for a missing tracker")
	}
}
