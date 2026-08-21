package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
)

type stubSearcher struct {
	results map[string][]mangabaka.Series
	calls   []string
}

func (s *stubSearcher) Search(_ context.Context, query string, _ int) ([]mangabaka.Series, error) {
	s.calls = append(s.calls, query)
	return s.results[query], nil
}

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

func seedTracker(t *testing.T, db *sql.DB, title string, relatedJSON *string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter, related_titles)
		VALUES (1, ?, (SELECT id FROM sources WHERE key = 'mangafire'), ?, 'reading', 10, ?)
	`, title, "https://mangafire.to/manga/"+title, relatedJSON)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seeded tracker id: %v", err)
	}
	return id
}

func relatedTitlesOf(t *testing.T, db *sql.DB, trackerID int64) []string {
	t.Helper()
	var raw sql.NullString
	if err := db.QueryRow(`SELECT related_titles FROM trackers WHERE id = ?`, trackerID).Scan(&raw); err != nil {
		t.Fatalf("read related titles: %v", err)
	}
	if !raw.Valid {
		return nil
	}
	return decodeRelatedTitles(raw.String)
}

func TestBackfillMergesOnlyExactMatches(t *testing.T) {
	db := openTestDB(t)
	exactID := seedTracker(t, db, "Solo Leveling", nil)
	nearID := seedTracker(t, db, "Nano Machine", nil)

	aid := &stubSearcher{results: map[string][]mangabaka.Series{
		"Solo Leveling": {{
			ID:     1,
			Title:  "Solo Leveling",
			Titles: []string{"Solo Leveling", "Only I Level Up", "나 혼자만 레벨업"},
		}},
		// Similar but never exactly equal: must be rejected.
		"Nano Machine": {{
			ID:     2,
			Title:  "Nano Machines Inc",
			Titles: []string{"Nano Machines Inc"},
		}},
	}}

	result, err := runBackfill(db, aid, 0, 0, 0, false)
	if err != nil {
		t.Fatalf("run backfill: %v", err)
	}

	if result.Total != 2 || result.Matched != 1 || result.NoRecord != 1 {
		t.Fatalf("unexpected stats: %+v", result)
	}
	if result.TitlesAdded != 1 {
		t.Fatalf("expected 1 added title, got %d", result.TitlesAdded)
	}

	got := relatedTitlesOf(t, db, exactID)
	// The record's own main title is stored too (MergeRelatedTitles semantics);
	// the non-latin one is filtered.
	want := []string{"Solo Leveling", "Only I Level Up"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected related titles: got %v want %v", got, want)
	}

	if titles := relatedTitlesOf(t, db, nearID); titles != nil {
		t.Fatalf("near match must not merge, got %v", titles)
	}
}

func TestBackfillFallsBackToStoredAlternateQueries(t *testing.T) {
	db := openTestDB(t)
	related := `["Second Name"]`
	trackerID := seedTracker(t, db, "Wrong Name", &related)

	aid := &stubSearcher{results: map[string][]mangabaka.Series{
		"Second Name": {{
			ID:     3,
			Title:  "Second Name",
			Titles: []string{"Second Name", "Third Name"},
		}},
	}}

	result, err := runBackfill(db, aid, 0, 0, 0, false)
	if err != nil {
		t.Fatalf("run backfill: %v", err)
	}
	if result.Matched != 1 || result.TitlesAdded != 1 {
		t.Fatalf("unexpected stats: %+v", result)
	}
	if got := relatedTitlesOf(t, db, trackerID); !reflect.DeepEqual(got, []string{"Second Name", "Third Name"}) {
		t.Fatalf("unexpected related titles: %v", got)
	}
	if !reflect.DeepEqual(aid.calls, []string{"Wrong Name", "Second Name"}) {
		t.Fatalf("unexpected query order: %v", aid.calls)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	trackerID := seedTracker(t, db, "Solo Leveling", nil)

	aid := &stubSearcher{results: map[string][]mangabaka.Series{
		"Solo Leveling": {{
			ID:     1,
			Title:  "Solo Leveling",
			Titles: []string{"Solo Leveling", "Only I Level Up"},
		}},
	}}

	if _, err := runBackfill(db, aid, 0, 0, 0, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := relatedTitlesOf(t, db, trackerID)

	second, err := runBackfill(db, aid, 0, 0, 0, false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.TitlesAdded != 0 || second.Matched != 0 || second.Unchanged != 1 {
		t.Fatalf("second run must be a no-op, got %+v", second)
	}
	if got := relatedTitlesOf(t, db, trackerID); !reflect.DeepEqual(got, first) {
		t.Fatalf("second run changed titles: %v vs %v", got, first)
	}
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	db := openTestDB(t)
	trackerID := seedTracker(t, db, "Solo Leveling", nil)

	aid := &stubSearcher{results: map[string][]mangabaka.Series{
		"Solo Leveling": {{
			ID:     1,
			Title:  "Solo Leveling",
			Titles: []string{"Solo Leveling", "Only I Level Up"},
		}},
	}}

	result, err := runBackfill(db, aid, 0, 0, 0, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Matched != 1 || result.TitlesAdded != 1 {
		t.Fatalf("dry run must still report the merge: %+v", result)
	}
	if titles := relatedTitlesOf(t, db, trackerID); titles != nil {
		t.Fatalf("dry run must not write, got %v", titles)
	}
}
