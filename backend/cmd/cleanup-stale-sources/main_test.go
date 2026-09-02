package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
)

// openTestDB opens through database.Open so the suite runs under the exact
// production configuration — single connection, WAL, foreign keys on. That
// matters here beyond fidelity: this tool once issued a nested query while an
// outer rows cursor was still streaming, which with one connection waits
// forever for a connection only that cursor can release. The tool deadlocked
// the moment any tracker still had a stale primary source, and only ever
// "worked" because trackers had been relinked away from retired sources
// first. Running the tests on a two-connection pool would hide a regression;
// on this one it hangs until the go test timeout, which is a loud enough
// failure.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := database.SeedDefaults(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	// The deployed database still carries the rows of the two sources retired
	// in 2026-08: disabled, never deleted, because removing them is this tool's
	// job. A fresh schema has no such history, so the suite plants them; every
	// test below relies on exactly these two being the stale set.
	if _, err := db.Exec(`
		INSERT INTO sources (key, name, connector_kind, enabled)
		VALUES ('mangabuddy', 'MangaBuddy', 'native', 0),
		       ('weebcentral', 'WeebCentral', 'native', 0)
	`); err != nil {
		t.Fatalf("plant retired sources: %v", err)
	}
	return db
}

func sourceIDByKey(t *testing.T, db *sql.DB, key string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, key).Scan(&id); err != nil {
		t.Fatalf("look up source %q: %v", key, err)
	}
	return id
}

func seedSource(t *testing.T, db *sql.DB, key string, name string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO sources (key, name, connector_kind, enabled)
		VALUES (?, ?, 'native', 1)
	`, key, name)
	if err != nil {
		t.Fatalf("seed source %q: %v", key, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seeded source id: %v", err)
	}
	return id
}

func seedTracker(t *testing.T, db *sql.DB, title string, sourceID int64, sourceURL string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, ?, ?, ?, 'reading')
	`, title, sourceID, sourceURL)
	if err != nil {
		t.Fatalf("seed tracker %q: %v", title, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seeded tracker id: %v", err)
	}
	return id
}

func seedTrackerSource(t *testing.T, db *sql.DB, trackerID int64, sourceID int64, sourceURL string, sourceItemID *string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_url, source_item_id)
		VALUES (?, ?, ?, ?)
	`, trackerID, sourceID, sourceURL, sourceItemID); err != nil {
		t.Fatalf("seed tracker_source tracker=%d source=%d: %v", trackerID, sourceID, err)
	}
}

func seedSuggestion(t *testing.T, db *sql.DB, trackerID int64, sourceID int64, candidateURL string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO source_link_suggestions (tracker_id, source_id, candidate_url, candidate_title)
		VALUES (?, ?, ?, 'candidate')
	`, trackerID, sourceID, candidateURL); err != nil {
		t.Fatalf("seed suggestion tracker=%d source=%d: %v", trackerID, sourceID, err)
	}
}

func seedLogo(t *testing.T, db *sql.DB, sourceID int64, logoURL string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO source_logos (source_id, logo_url)
		VALUES (?, ?)
	`, sourceID, logoURL); err != nil {
		t.Fatalf("seed logo source=%d: %v", sourceID, err)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return count
}

func TestBuildActiveSourceKeySetCoversSeededSources(t *testing.T) {
	active := buildActiveSourceKeySet()

	for _, key := range []string{
		"mangadex", "mangafire", "asuracomic", "flamecomics", "mgeko",
		"webtoons", "freewebnovel", "mangaupdates", "comick", "mangahub",
	} {
		if _, ok := active[key]; !ok {
			t.Errorf("registry is missing seeded source key %q; every seeded source would be treated as stale", key)
		}
	}

	for _, retired := range []string{"mangabuddy", "weebcentral", "mangaplus"} {
		if _, ok := active[retired]; ok {
			t.Errorf("retired source %q must not be in the active set", retired)
		}
	}
}

func TestListStaleSourcesFindsOnlyTheRetiredRows(t *testing.T) {
	db := openTestDB(t)

	// The retired sources were disabled rather than deleted and their removal
	// left to this tool, so the stale set is exactly those two (ordered by key)
	// and none of the seeded ones.
	stale, byID, err := listStaleSources(db, buildActiveSourceKeySet())
	if err != nil {
		t.Fatalf("list stale sources: %v", err)
	}
	if len(stale) != 2 || stale[0].Key != "mangabuddy" || stale[1].Key != "weebcentral" {
		t.Fatalf("expected exactly the retired mangabuddy+weebcentral rows, got %+v", stale)
	}
	if stale[0].Enabled || stale[1].Enabled {
		t.Fatalf("retired sources must be disabled, got %+v", stale)
	}
	if byID[stale[0].ID] != "mangabuddy" || byID[stale[1].ID] != "weebcentral" {
		t.Fatalf("byID map mismatch: %v", byID)
	}
}

func TestListStaleSourcesReportsUsageCounts(t *testing.T) {
	db := openTestDB(t)

	staleID := sourceIDByKey(t, db, "mangabuddy")
	activeID := sourceIDByKey(t, db, "mangadex")

	t1 := seedTracker(t, db, "Alpha", staleID, "https://mangabuddy.com/alpha")
	seedTracker(t, db, "Beta", staleID, "https://mangabuddy.com/beta")
	t3 := seedTracker(t, db, "Gamma", activeID, "https://mangadex.org/title/gamma")
	seedTrackerSource(t, db, t1, staleID, "https://mangabuddy.com/alpha", nil)
	seedTrackerSource(t, db, t3, staleID, "https://mangabuddy.com/gamma", nil)
	seedLogo(t, db, staleID, "https://mangabuddy.com/logo.png")

	stale, byID, err := listStaleSources(db, buildActiveSourceKeySet())
	if err != nil {
		t.Fatalf("list stale sources: %v", err)
	}
	// mangabuddy plus the zero-usage weebcentral stray, ordered by key.
	if len(stale) != 2 {
		t.Fatalf("expected two stale sources, got %+v", stale)
	}

	got := stale[0]
	if got.ID != staleID || got.Key != "mangabuddy" || got.Name != "MangaBuddy" {
		t.Fatalf("unexpected stale source identity: %+v", got)
	}
	if got.PrimaryTrackers != 2 || got.LinkedSources != 2 || got.Logos != 1 {
		t.Fatalf("unexpected usage counts: %+v", got)
	}
	if byID[staleID] != "mangabuddy" {
		t.Fatalf("byID map missing stale source: %v", byID)
	}
}

func TestListStaleSourcesNormalizesKeysCaseInsensitively(t *testing.T) {
	db := openTestDB(t)

	// A key that differs only in case/whitespace from an active key is NOT
	// stale; matching is on the normalized (lowercased, trimmed) key. Only the
	// two retired rows remain stale.
	casedID := seedSource(t, db, " ComicK ", "ComicK Cased")

	stale, _, err := listStaleSources(db, buildActiveSourceKeySet())
	if err != nil {
		t.Fatalf("list stale sources: %v", err)
	}
	if len(stale) != 2 || stale[0].Key != "mangabuddy" || stale[1].Key != "weebcentral" {
		t.Fatalf("cased active source %d must be spared; got %+v", casedID, stale)
	}
}

func TestPlanTrackerPrimarySourcePromotions(t *testing.T) {
	db := openTestDB(t)

	staleA := sourceIDByKey(t, db, "mangabuddy")
	staleB := sourceIDByKey(t, db, "weebcentral")
	activeID := sourceIDByKey(t, db, "mangadex")

	// Promotable: primary on staleA; the first linked row points at another
	// stale source and must be skipped, the second is active and wins.
	promotable := seedTracker(t, db, "Promotable", staleA, "https://mangabuddy.com/promotable")
	seedTrackerSource(t, db, promotable, staleB, "https://weebcentral.com/promotable", nil)
	itemID := "  md-item-1  "
	seedTrackerSource(t, db, promotable, activeID, "  https://mangadex.org/title/promotable  ", &itemID)

	// Orphaned: only stale links.
	orphan := seedTracker(t, db, "Orphan", staleA, "https://mangabuddy.com/orphan")
	seedTrackerSource(t, db, orphan, staleB, "https://weebcentral.com/orphan", nil)

	// Blank-URL candidate is unusable; tracker stays orphaned.
	blank := seedTracker(t, db, "BlankURL", staleB, "https://weebcentral.com/blank")
	seedTrackerSource(t, db, blank, activeID, "   ", nil)

	staleIDs := map[int64]struct{}{staleA: {}, staleB: {}}
	staleKeys := map[int64]string{staleA: "mangabuddy", staleB: "weebcentral"}

	promotions, orphaned, err := planTrackerPrimarySourcePromotions(db, staleIDs, staleKeys)
	if err != nil {
		t.Fatalf("plan promotions: %v", err)
	}

	if len(promotions) != 1 {
		t.Fatalf("expected one promotion, got %+v", promotions)
	}
	p := promotions[0]
	if p.TrackerID != promotable || p.OldSourceID != staleA || p.OldSourceKey != "mangabuddy" {
		t.Fatalf("unexpected promotion origin: %+v", p)
	}
	if p.NewSourceID != activeID || p.NewSourceKey != "mangadex" {
		t.Fatalf("unexpected promotion target: %+v", p)
	}
	if p.NewSourceURL != "https://mangadex.org/title/promotable" {
		t.Fatalf("candidate URL must be trimmed, got %q", p.NewSourceURL)
	}
	if p.NewSourceItemID == nil || *p.NewSourceItemID != "md-item-1" {
		t.Fatalf("candidate item id must be trimmed, got %#v", p.NewSourceItemID)
	}

	if len(orphaned) != 2 {
		t.Fatalf("expected two orphaned trackers, got %+v", orphaned)
	}
	if orphaned[0].TrackerID != orphan || orphaned[0].SourceKey != "mangabuddy" {
		t.Fatalf("unexpected first orphan: %+v", orphaned[0])
	}
	if orphaned[1].TrackerID != blank || orphaned[1].SourceKey != "weebcentral" {
		t.Fatalf("unexpected second orphan: %+v", orphaned[1])
	}
}

// Replacements for every affected tracker are read in one query, so the rows
// of different trackers arrive interleaved and a candidate could leak from one
// tracker to the next. Each tracker must still get the first usable link of
// its own, by ts.id.
func TestPlanTrackerPrimarySourcePromotionsPicksPerTrackerCandidate(t *testing.T) {
	db := openTestDB(t)

	staleA := sourceIDByKey(t, db, "mangabuddy")
	staleB := sourceIDByKey(t, db, "weebcentral")
	mangadexID := sourceIDByKey(t, db, "mangadex")
	comickID := sourceIDByKey(t, db, "comick")

	first := seedTracker(t, db, "First", staleA, "https://mangabuddy.com/first")
	second := seedTracker(t, db, "Second", staleA, "https://mangabuddy.com/second")

	// Seeded so the two trackers' link rows alternate by ts.id: the first
	// tracker's winner (mangadex) is inserted after the second tracker's winner
	// (comick).
	seedTrackerSource(t, db, first, staleB, "https://weebcentral.com/first", nil)
	seedTrackerSource(t, db, second, comickID, "https://comick.io/comic/second", nil)
	seedTrackerSource(t, db, first, mangadexID, "https://mangadex.org/title/first", nil)
	seedTrackerSource(t, db, second, mangadexID, "https://mangadex.org/title/second", nil)
	seedTrackerSource(t, db, first, comickID, "https://comick.io/comic/first", nil)

	staleIDs := map[int64]struct{}{staleA: {}, staleB: {}}
	staleKeys := map[int64]string{staleA: "mangabuddy", staleB: "weebcentral"}

	promotions, orphaned, err := planTrackerPrimarySourcePromotions(db, staleIDs, staleKeys)
	if err != nil {
		t.Fatalf("plan promotions: %v", err)
	}
	if len(orphaned) != 0 {
		t.Fatalf("expected no orphans, got %+v", orphaned)
	}
	if len(promotions) != 2 {
		t.Fatalf("expected two promotions, got %+v", promotions)
	}

	if promotions[0].TrackerID != first || promotions[0].NewSourceID != mangadexID {
		t.Fatalf("tracker %d must be promoted to mangadex, got %+v", first, promotions[0])
	}
	if promotions[0].NewSourceURL != "https://mangadex.org/title/first" {
		t.Fatalf("first tracker got another tracker's URL: %+v", promotions[0])
	}
	if promotions[1].TrackerID != second || promotions[1].NewSourceID != comickID {
		t.Fatalf("tracker %d must be promoted to comick, got %+v", second, promotions[1])
	}
	if promotions[1].NewSourceURL != "https://comick.io/comic/second" {
		t.Fatalf("second tracker got another tracker's URL: %+v", promotions[1])
	}
}

func TestPlanTrackerPrimarySourcePromotionsEmptyInput(t *testing.T) {
	db := openTestDB(t)

	promotions, orphaned, err := planTrackerPrimarySourcePromotions(db, map[int64]struct{}{}, map[int64]string{})
	if err != nil {
		t.Fatalf("plan promotions: %v", err)
	}
	if len(promotions) != 0 || len(orphaned) != 0 {
		t.Fatalf("expected empty plan, got %+v / %+v", promotions, orphaned)
	}
}

// seedCleanupScenario builds the canonical mixed scenario on top of the
// retired rows openTestDB plants (mangabuddy carries all the usage,
// weebcentral stays a zero-usage stray):
//   - promotable: primary stale, linked to mangadex (survives, repointed);
//   - orphan: primary stale, no active links (deleted);
//   - bystander: primary mangadex, linked to stale, reading pin on stale,
//     one suggestion on stale and one on mangadex (survives, stale bits pruned).
func seedCleanupScenario(t *testing.T, db *sql.DB) (staleID, activeID, promotable, orphan, bystander int64) {
	t.Helper()

	staleID = sourceIDByKey(t, db, "mangabuddy")
	activeID = sourceIDByKey(t, db, "mangadex")

	promotable = seedTracker(t, db, "Promotable", staleID, "https://mangabuddy.com/promotable")
	seedTrackerSource(t, db, promotable, staleID, "https://mangabuddy.com/promotable", nil)
	itemID := " md-promotable "
	seedTrackerSource(t, db, promotable, activeID, "https://mangadex.org/title/promotable", &itemID)
	seedSuggestion(t, db, promotable, activeID, "https://mangadex.org/title/promotable-suggestion")

	orphan = seedTracker(t, db, "Orphan", staleID, "https://mangabuddy.com/orphan")
	seedTrackerSource(t, db, orphan, staleID, "https://mangabuddy.com/orphan", nil)
	seedSuggestion(t, db, orphan, staleID, "https://mangabuddy.com/orphan-suggestion")

	bystander = seedTracker(t, db, "Bystander", activeID, "https://mangadex.org/title/bystander")
	seedTrackerSource(t, db, bystander, activeID, "https://mangadex.org/title/bystander", nil)
	seedTrackerSource(t, db, bystander, staleID, "https://mangabuddy.com/bystander", nil)
	seedSuggestion(t, db, bystander, staleID, "https://mangabuddy.com/bystander-suggestion")
	seedSuggestion(t, db, bystander, activeID, "https://mangadex.org/title/bystander-suggestion")
	if _, err := db.Exec(`UPDATE trackers SET reading_source_id = ? WHERE id = ?`, staleID, bystander); err != nil {
		t.Fatalf("pin bystander reading source: %v", err)
	}

	seedLogo(t, db, staleID, "https://mangabuddy.com/logo.png")
	seedLogo(t, db, activeID, "https://mangadex.org/logo.png")

	return staleID, activeID, promotable, orphan, bystander
}

func snapshotCounts(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64)
	for _, table := range []string{"sources", "trackers", "tracker_sources", "source_link_suggestions", "source_logos"} {
		counts[table] = countRows(t, db, "SELECT COUNT(1) FROM "+table)
	}
	return counts
}

func TestDryRunPlanningReportsWithoutWriting(t *testing.T) {
	db := openTestDB(t)
	staleID, _, promotable, orphan, _ := seedCleanupScenario(t, db)

	before := snapshotCounts(t, db)

	stale, byID, err := listStaleSources(db, buildActiveSourceKeySet())
	if err != nil {
		t.Fatalf("list stale sources: %v", err)
	}
	staleIDs := map[int64]struct{}{}
	for _, source := range stale {
		staleIDs[source.ID] = struct{}{}
	}
	promotions, orphaned, err := planTrackerPrimarySourcePromotions(db, staleIDs, byID)
	if err != nil {
		t.Fatalf("plan promotions: %v", err)
	}

	// The dry-run report must be accurate... (mangabuddy carries the usage,
	// weebcentral is the second, zero-usage retired row)
	if len(stale) != 2 || stale[0].ID != staleID {
		t.Fatalf("expected stale sources led by %d, got %+v", staleID, stale)
	}
	if stale[0].PrimaryTrackers != 2 || stale[0].LinkedSources != 3 || stale[0].Logos != 1 {
		t.Fatalf("unexpected reported usage counts: %+v", stale[0])
	}
	if got := sumLinkedRowCounts(stale); got != 3 {
		t.Fatalf("sumLinkedRowCounts = %d, want 3", got)
	}
	if len(promotions) != 1 || promotions[0].TrackerID != promotable {
		t.Fatalf("expected promotable tracker %d in promotions, got %+v", promotable, promotions)
	}
	if len(orphaned) != 1 || orphaned[0].TrackerID != orphan {
		t.Fatalf("expected orphan tracker %d, got %+v", orphan, orphaned)
	}

	// ...and planning alone must not touch the database.
	after := snapshotCounts(t, db)
	for table, count := range before {
		if after[table] != count {
			t.Errorf("dry-run planning modified %s: %d -> %d", table, count, after[table])
		}
	}
	var trackerSourceID int64
	if err := db.QueryRow(`SELECT source_id FROM trackers WHERE id = ?`, promotable).Scan(&trackerSourceID); err != nil {
		t.Fatalf("read promotable tracker: %v", err)
	}
	if trackerSourceID != staleID {
		t.Fatalf("dry-run planning must not promote trackers, primary moved to %d", trackerSourceID)
	}
}

func TestApplyCleanupDeletesExactlyStaleEntities(t *testing.T) {
	db := openTestDB(t)
	staleID, activeID, promotable, orphan, bystander := seedCleanupScenario(t, db)

	// The bystander's stored chapter number is credited to the stale source.
	// The column has no foreign key, so the cleanup has to clear it by hand.
	if _, err := db.Exec(`UPDATE trackers SET latest_chapter_source_id = ? WHERE id = ?`, staleID, bystander); err != nil {
		t.Fatalf("seed chapter reporter: %v", err)
	}

	stale, byID, err := listStaleSources(db, buildActiveSourceKeySet())
	if err != nil {
		t.Fatalf("list stale sources: %v", err)
	}
	staleIDs := map[int64]struct{}{}
	for _, source := range stale {
		staleIDs[source.ID] = struct{}{}
	}
	promotions, _, err := planTrackerPrimarySourcePromotions(db, staleIDs, byID)
	if err != nil {
		t.Fatalf("plan promotions: %v", err)
	}

	outcome, err := applyCleanup(db, stale, promotions)
	if err != nil {
		t.Fatalf("apply cleanup: %v", err)
	}

	want := cleanupOutcome{
		PromotedTrackers: 1,
		DeletedTrackers:  1, // only the orphan; the promotable tracker was repointed first
		DeletedLinks:     3, // promotable->stale, orphan->stale, bystander->stale
		// The orphan's suggestion cascades with the tracker delete; only the
		// surviving bystander's stale suggestion is counted here.
		DeletedSuggestions:      1,
		ClearedReadingPins:      1,
		ClearedChapterReporters: 1,
		DeletedSourceLogos:      1,
		DeletedSources:          2, // mangabuddy plus the zero-usage weebcentral stray
	}
	if outcome != want {
		t.Fatalf("outcome = %+v, want %+v", outcome, want)
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM trackers WHERE latest_chapter_source_id = ?`, staleID); got != 0 {
		t.Fatalf("a tracker still credits its chapter to the deleted source")
	}

	// The stale source and everything anchored to it is gone.
	if got := countRows(t, db, `SELECT COUNT(1) FROM sources WHERE id = ?`, staleID); got != 0 {
		t.Fatalf("stale source survived")
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM trackers WHERE id = ?`, orphan); got != 0 {
		t.Fatalf("orphaned tracker survived")
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE source_id = ?`, staleID); got != 0 {
		t.Fatalf("stale tracker_sources rows survived")
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM source_link_suggestions WHERE source_id = ?`, staleID); got != 0 {
		t.Fatalf("stale suggestions survived")
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM source_logos WHERE source_id = ?`, staleID); got != 0 {
		t.Fatalf("stale logo survived")
	}

	// The promoted tracker survives, repointed at the active source with
	// trimmed URL and item id.
	var (
		gotSourceID int64
		gotItemID   sql.NullString
		gotURL      string
	)
	if err := db.QueryRow(`
		SELECT source_id, source_item_id, source_url FROM trackers WHERE id = ?
	`, promotable).Scan(&gotSourceID, &gotItemID, &gotURL); err != nil {
		t.Fatalf("read promoted tracker: %v", err)
	}
	if gotSourceID != activeID || gotURL != "https://mangadex.org/title/promotable" {
		t.Fatalf("promoted tracker not repointed: source=%d url=%q", gotSourceID, gotURL)
	}
	if !gotItemID.Valid || gotItemID.String != "md-promotable" {
		t.Fatalf("promoted tracker item id = %#v, want trimmed md-promotable", gotItemID)
	}

	// Non-stale entities survive untouched.
	var pin sql.NullInt64
	if err := db.QueryRow(`SELECT reading_source_id FROM trackers WHERE id = ?`, bystander).Scan(&pin); err != nil {
		t.Fatalf("read bystander tracker: %v", err)
	}
	if pin.Valid {
		t.Fatalf("bystander reading pin to the stale source must be cleared, got %#v", pin)
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM trackers WHERE id = ?`, bystander); got != 1 {
		t.Fatalf("bystander tracker deleted")
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM tracker_sources WHERE source_id = ?`, activeID); got != 2 {
		t.Fatalf("active-source links must survive, got %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM source_link_suggestions WHERE source_id = ?`, activeID); got != 2 {
		t.Fatalf("active-source suggestions must survive, got %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM source_logos WHERE source_id = ?`, activeID); got != 1 {
		t.Fatalf("active-source logo must survive, got %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(1) FROM sources WHERE id = ?`, activeID); got != 1 {
		t.Fatalf("active source deleted")
	}

	// Running the cycle again finds nothing left to clean.
	staleAfter, _, err := listStaleSources(db, buildActiveSourceKeySet())
	if err != nil {
		t.Fatalf("re-list stale sources: %v", err)
	}
	if len(staleAfter) != 0 {
		t.Fatalf("expected no stale sources after cleanup, got %+v", staleAfter)
	}
}

func TestApplyCleanupNoStaleSourcesIsNoOp(t *testing.T) {
	db := openTestDB(t)
	seedCleanupScenario(t, db)

	before := snapshotCounts(t, db)
	outcome, err := applyCleanup(db, nil, nil)
	if err != nil {
		t.Fatalf("apply cleanup: %v", err)
	}
	if outcome != (cleanupOutcome{}) {
		t.Fatalf("expected zero outcome, got %+v", outcome)
	}
	after := snapshotCounts(t, db)
	for table, count := range before {
		if after[table] != count {
			t.Errorf("no-op cleanup modified %s: %d -> %d", table, count, after[table])
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := placeholders(0); got != "" {
		t.Errorf("placeholders(0) = %q, want empty", got)
	}
	if got := placeholders(3); got != "?,?,?" {
		t.Errorf("placeholders(3) = %q", got)
	}
	if got := normalizeSourceKey("  MangaFire "); got != "mangafire" {
		t.Errorf("normalizeSourceKey = %q, want mangafire", got)
	}
}
