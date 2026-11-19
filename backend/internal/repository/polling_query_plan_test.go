package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// explainQueryPlan returns the detail column of every EXPLAIN QUERY PLAN row.
func explainQueryPlan(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN ` + query)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0)
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return details
}

// TestListForPollingFindsTheMirrorRowThroughTheTrackersOwnLinks pins the plan
// of the mirror-row subquery. On the deployed database it ran once per tracker
// through idx_tracker_sources_source_id — every link of the tracker's site,
// hundreds of rows — where the tracker's own one to five links are reached
// through the UNIQUE(tracker_id, source_id, source_url) index. The schema here
// comes from the same migrations and carries no sqlite_stat tables either, so
// the planner decides on what it would see in production: before the fix this
// test failed with the very plan the deployed database reported.
func TestListForPollingFindsTheMirrorRowThroughTheTrackersOwnLinks(t *testing.T) {
	db := openPollingTestDB(t)

	for index := 0; index < 6; index++ {
		url := fmt.Sprintf("https://mangadex.org/title/plan-%d", index)
		result, err := db.Exec(`
			INSERT INTO trackers (title, source_id, source_url, status, latest_known_chapter)
			VALUES (?, (SELECT id FROM sources WHERE key = 'mangadex'), ?, 'reading', 10)
		`, fmt.Sprintf("Plan %d", index), url)
		if err != nil {
			t.Fatalf("seed tracker: %v", err)
		}
		trackerID, _ := result.LastInsertId()
		if _, err := db.Exec(`
			INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES
				(?, (SELECT id FROM sources WHERE key = 'mangadex'), ?),
				(?, (SELECT id FROM sources WHERE key = 'mangafire'), ?)
		`, trackerID, url, trackerID, fmt.Sprintf("https://mangafire.to/title/plan-%d", index)); err != nil {
			t.Fatalf("seed links: %v", err)
		}
	}

	plan := explainQueryPlan(t, db, repository.PollingTrackersQueryForTest)
	joined := strings.Join(plan, "\n")
	if strings.Contains(joined, "idx_tracker_sources_source_id") {
		t.Fatalf("the mirror row is looked up through every link of the site:\n%s", joined)
	}
	viaOwnLinks := false
	for _, detail := range plan {
		if strings.Contains(detail, "SEARCH m ") && strings.Contains(detail, "sqlite_autoindex_tracker_sources_1 (tracker_id=?") {
			viaOwnLinks = true
		}
	}
	if !viaOwnLinks {
		t.Fatalf("the mirror row is not looked up through the tracker's own links:\n%s", joined)
	}
}

// explainWith is explainQueryPlan for a statement that binds arguments.
func explainWith(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0)
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return strings.Join(details, "\n")
}

// TestListAndCountByStatusUseTheProfileStatusIndex pins that migration 0004's
// index is what the planner takes for the list every dashboard page asks for,
// a profile's trackers in one reading status, and answers the count from the
// index alone. The statements are shaped as TrackerRepository.Count and List
// build them.
func TestListAndCountByStatusUseTheProfileStatusIndex(t *testing.T) {
	db := openPollingTestDB(t)

	count := explainWith(t, db, `SELECT COUNT(1) FROM trackers WHERE profile_id = ? AND status IN (?)`, 1, "reading")
	if !strings.Contains(count, "USING COVERING INDEX idx_trackers_profile_status (profile_id=? AND status=?)") {
		t.Errorf("the count is not answered from the index:\n%s", count)
	}
	list := explainWith(t, db, `SELECT id, title FROM trackers WHERE profile_id = ? AND status IN (?) ORDER BY created_at DESC, id DESC LIMIT ?`, 1, "reading", 24)
	if !strings.Contains(list, "idx_trackers_profile_status (profile_id=? AND status=?)") {
		t.Errorf("the list does not find its trackers through the index:\n%s", list)
	}
}

// TestPinnedPlansHoldOnceTheDatabaseHasStatistics: the server now leaves the
// planner statistics when it shuts down (database.Optimize), so the plans the
// tests above pin on a database without any must also be the plans it picks
// with them. A library-shaped set of rows, analyzed, and the same checks.
func TestPinnedPlansHoldOnceTheDatabaseHasStatistics(t *testing.T) {
	db := openPollingTestDB(t)

	for index := 0; index < 60; index++ {
		url := fmt.Sprintf("https://mangadex.org/title/stats-%d", index)
		result, err := db.Exec(`
			INSERT INTO trackers (title, source_id, source_url, status, latest_known_chapter)
			VALUES (?, (SELECT id FROM sources WHERE key = 'mangadex'), ?, 'reading', 10)
		`, fmt.Sprintf("Stats %d", index), url)
		if err != nil {
			t.Fatalf("seed tracker: %v", err)
		}
		trackerID, _ := result.LastInsertId()
		if _, err := db.Exec(`
			INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES
				(?, (SELECT id FROM sources WHERE key = 'mangadex'), ?),
				(?, (SELECT id FROM sources WHERE key = 'mangafire'), ?)
		`, trackerID, url, trackerID, fmt.Sprintf("https://mangafire.to/title/stats-%d", index)); err != nil {
			t.Fatalf("seed links: %v", err)
		}
		if index%2 == 0 {
			if _, err := db.Exec(`
				INSERT INTO source_metadata (source_id, source_url, payload, fetched_at)
				VALUES ((SELECT id FROM sources WHERE key = 'mangadex'), ?, '{"type":"manga"}', '2026-11-01 00:00:00')
			`, url); err != nil {
				t.Fatalf("seed metadata: %v", err)
			}
		}
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var stats int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_stat1`).Scan(&stats); err != nil || stats == 0 {
		t.Fatalf("precondition: the database has statistics now, got %d rows, %v", stats, err)
	}

	polling := strings.Join(explainQueryPlan(t, db, repository.PollingTrackersQueryForTest), "\n")
	if strings.Contains(polling, "idx_tracker_sources_source_id") ||
		!strings.Contains(polling, "sqlite_autoindex_tracker_sources_1 (tracker_id=?") {
		t.Fatalf("with statistics the mirror row is no longer looked up through the tracker's own links:\n%s", polling)
	}

	// The backfill listing must still settle "has metadata" on an index of
	// source_metadata alone, without reading the rows.
	for _, detail := range explainQueryPlan(t, db, repository.MetadataBackfillLinksQueryForTest) {
		if strings.Contains(detail, " sm ") && !strings.Contains(detail, "USING COVERING INDEX") {
			t.Fatalf("with statistics the backfill listing reads stored metadata rows: %s", detail)
		}
	}
}

// TestListForPollingReadsTheLowestMatchingMirrorRow pins what the subquery
// selects, which the plan must not change: among the tracker's links, the
// lowest-id row on the primary's site whose URL matches the primary's ignoring
// case and surrounding whitespace — never an alternate, never another
// tracker's row on the same site.
func TestListForPollingReadsTheLowestMatchingMirrorRow(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	trackerID := seedPollingTracker(t, db, 19)
	otherID := seedPollingTracker(t, db, 7)

	first := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	elsewhere := first.Add(2 * time.Hour)
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url, last_polled_at) VALUES
			(?, (SELECT id FROM sources WHERE key = 'mangafire'), ?, ?),
			(?, 1, ?, ?),
			(?, 1, ?, ?),
			(?, 1, ?, ?)
	`,
		trackerID, alternateURL, elsewhere,
		trackerID, "  "+strings.ToUpper(pollingSeedURL[:8])+pollingSeedURL[8:]+" ", first,
		trackerID, pollingSeedURL, second,
		otherID, pollingSeedURL+"-other", elsewhere,
	); err != nil {
		t.Fatalf("seed links: %v", err)
	}

	trackers, err := repo.ListForPolling(context.Background())
	if err != nil {
		t.Fatalf("list for polling: %v", err)
	}
	if len(trackers) != 2 {
		t.Fatalf("expected each tracker listed once, got %d rows", len(trackers))
	}
	for _, tracker := range trackers {
		switch tracker.ID {
		case trackerID:
			if tracker.PrimaryLastPolledAt == nil || !tracker.PrimaryLastPolledAt.Equal(first) {
				t.Fatalf("expected the lowest-id matching mirror row's stamp %s, got %v", first, tracker.PrimaryLastPolledAt)
			}
		case otherID:
			if tracker.PrimaryLastPolledAt != nil {
				t.Fatalf("a tracker with no mirror row read someone else's stamp: %v", tracker.PrimaryLastPolledAt)
			}
		}
	}
}
