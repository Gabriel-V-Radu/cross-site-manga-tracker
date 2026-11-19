package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// meteredStore is the real repository with its listings counted, so a test
// can say not only what the service stored but what it read to get there:
// the payload-carrying listing is the expensive one, and the loop must not
// touch it unless a view has to be rebuilt.
type meteredStore struct {
	Store
	stampListings    int
	backfillListings int
	// payloadListings holds the tracker ids of every payload listing asked.
	payloadListings [][]int64
	// saved holds the tracker ids of every non-empty batch of views written.
	saved [][]int64
}

func (m *meteredStore) ListMetadataLinkStamps(ctx context.Context) ([]repository.MetadataLink, error) {
	m.stampListings++
	return m.Store.ListMetadataLinkStamps(ctx)
}

func (m *meteredStore) ListMetadataLinks(ctx context.Context, trackerIDs []int64) ([]repository.MetadataLink, error) {
	m.payloadListings = append(m.payloadListings, append([]int64(nil), trackerIDs...))
	return m.Store.ListMetadataLinks(ctx, trackerIDs)
}

func (m *meteredStore) ListMetadataBackfillLinks(ctx context.Context) ([]repository.MetadataLink, error) {
	m.backfillListings++
	links, err := m.Store.ListMetadataBackfillLinks(ctx)
	for _, link := range links {
		if link.Payload != "" {
			return nil, errors.New("the backfill listing carried a payload")
		}
	}
	return links, err
}

func (m *meteredStore) SaveTrackerMetadata(ctx context.Context, rows []repository.TrackerMetadataRow) error {
	if len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.TrackerID)
		}
		m.saved = append(m.saved, ids)
	}
	return m.Store.SaveTrackerMetadata(ctx, rows)
}

func (m *meteredStore) reset() {
	m.stampListings, m.backfillListings, m.payloadListings, m.saved = 0, 0, nil, nil
}

// askedConnector records which series it was asked to resolve. With answer
// unset it resolves nothing, the way a site that has no metadata for a series
// (or is failing) leaves the backfill empty-handed.
type askedConnector struct {
	key      string
	answer   *connectors.SeriesMetadata
	asked    []string
	cooldown time.Duration
	// failWith, when set, is what a resolve fails with in place of the plain
	// "nothing to say".
	failWith error
}

func (c *askedConnector) Key() string                       { return c.key }
func (c *askedConnector) Name() string                      { return c.key }
func (c *askedConnector) Kind() string                      { return connectors.KindNative }
func (c *askedConnector) HealthCheck(context.Context) error { return nil }
func (c *askedConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (c *askedConnector) ResolveByURL(_ context.Context, rawURL string) (*connectors.MangaResult, error) {
	c.asked = append(c.asked, rawURL)
	if c.failWith != nil {
		return nil, c.failWith
	}
	if c.answer == nil {
		return nil, errors.New("nothing to say")
	}
	return &connectors.MangaResult{SourceKey: c.key, URL: rawURL, Metadata: c.answer}, nil
}
func (c *askedConnector) CooldownRemaining() (time.Duration, string) {
	return c.cooldown, "test cooldown"
}

func insertTracker(t *testing.T, db *sql.DB, title string, sourceID int64, url string) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO trackers (profile_id, title, source_id, source_url, status) VALUES (1, ?, ?, ?, 'reading')`, title, sourceID, url)
	if err != nil {
		t.Fatalf("insert tracker %s: %v", title, err)
	}
	id, _ := result.LastInsertId()
	return id
}

func viewUpdatedAt(t *testing.T, db *sql.DB, trackerID int64) time.Time {
	t.Helper()
	var updatedAt time.Time
	if err := db.QueryRow(`SELECT updated_at FROM tracker_metadata WHERE tracker_id = ?`, trackerID).Scan(&updatedAt); err != nil {
		t.Fatalf("read view of tracker %d: %v", trackerID, err)
	}
	return updatedAt
}

// TestBackfillAsksOnlyForTheTrackerWithoutMetadata: the backfill now starts
// from a listing of the trackers that have nothing, instead of the whole
// library with its payloads. The tracker without metadata must still be
// reached, the one with metadata still left alone.
func TestBackfillAsksOnlyForTheTrackerWithoutMetadata(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex := sourceIDOf(t, db, "mangadex")
	const hasURL, lacksURL = "https://mangadex.org/title/has", "https://mangadex.org/title/lacks"
	has := insertTracker(t, db, "has", mangadex, hasURL)
	lacks := insertTracker(t, db, "lacks", mangadex, lacksURL)

	connector := &askedConnector{key: "mangadex", answer: &connectors.SeriesMetadata{Type: "manhwa", Genres: []string{"Action"}}}
	store := &meteredStore{Store: repository.NewMetadataRepository(db)}
	service := NewService(store, fakeResolver{"mangadex": connector}, Config{Backfill: true, BackfillGap: time.Millisecond}, nil)

	// The poller already brought the first tracker's metadata.
	service.Record(ctx, mangadex, hasURL, &connectors.SeriesMetadata{Type: "manga", Status: "ongoing"})
	if _, err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	store.reset()

	service.backfillPass(ctx)
	if len(connector.asked) != 1 || connector.asked[0] != lacksURL {
		t.Fatalf("the backfill asked about %v, want only the tracker without metadata", connector.asked)
	}
	if store.backfillListings != 1 || len(store.payloadListings) != 0 || store.stampListings != 0 {
		t.Fatalf("a backfill pass ran %d backfill, %d payload and %d stamp listings; want only its own",
			store.backfillListings, len(store.payloadListings), store.stampListings)
	}

	if written, err := service.Reconcile(ctx); err != nil || written != 1 {
		t.Fatalf("Reconcile = %d, %v; want the backfilled tracker's view", written, err)
	}
	repo := repository.NewMetadataRepository(db)
	if row, err := repo.GetTrackerMetadata(ctx, 1, lacks); err != nil || row == nil || row.SeriesType != "manhwa" {
		t.Fatalf("the backfilled tracker's view = %+v, %v", row, err)
	}
	if row, err := repo.GetTrackerMetadata(ctx, 1, has); err != nil || row == nil || row.SeriesType != "manga" {
		t.Fatalf("the other tracker's view = %+v, %v", row, err)
	}

	// With metadata everywhere the pass is one listing that comes back empty.
	store.reset()
	service.backfillPass(ctx)
	if len(connector.asked) != 1 {
		t.Fatalf("a tracker with metadata was asked about again: %v", connector.asked)
	}
	if store.backfillListings != 1 || len(store.payloadListings) != 0 {
		t.Fatalf("an idle backfill pass ran %d backfill and %d payload listings", store.backfillListings, len(store.payloadListings))
	}
}

// TestBackfillKeepsItsRetryWindowAndCooldownSkip pins the two things that
// stop the backfill from hammering a site, which the new listing sits in
// front of: a tracker whose resolve found nothing waits BackfillRetryAfter,
// and a site that is cooling down is not asked at all.
func TestBackfillKeepsItsRetryWindowAndCooldownSkip(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex, mangahub := sourceIDOf(t, db, "mangadex"), sourceIDOf(t, db, "mangahub")
	insertTracker(t, db, "silent", mangadex, "https://mangadex.org/title/silent")
	insertTracker(t, db, "cooling", mangahub, "https://mangahub.io/manga/cooling")

	silent := &askedConnector{key: "mangadex"}
	cooling := &askedConnector{key: "mangahub", cooldown: time.Hour, answer: &connectors.SeriesMetadata{Type: "manga"}}
	service := NewService(repository.NewMetadataRepository(db), fakeResolver{"mangadex": silent, "mangahub": cooling},
		Config{Backfill: true, BackfillGap: time.Millisecond, BackfillRetryAfter: time.Hour}, nil)
	clock := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }

	service.backfillPass(ctx)
	service.backfillPass(ctx)
	if len(silent.asked) != 1 {
		t.Fatalf("a tracker whose backfill found nothing was asked about %d times inside the retry window", len(silent.asked))
	}
	if len(cooling.asked) != 0 {
		t.Fatalf("a cooling-down site was asked: %v", cooling.asked)
	}

	clock = clock.Add(time.Hour)
	cooling.cooldown = 0
	service.backfillPass(ctx)
	if len(silent.asked) != 2 {
		t.Fatalf("past the retry window the tracker was asked about %d times in all, want 2", len(silent.asked))
	}
	if len(cooling.asked) != 1 {
		t.Fatalf("once the cooldown ended the site was asked %d times, want 1", len(cooling.asked))
	}
}

// TestBackfillAsksTheNextSiteWhenOneHasNothing: the retry window is a link's,
// not a tracker's. A tracker linked to a site with nothing to say about the
// series and to another that has it used to sit out the whole window after
// the first site came back empty, its second site never asked.
func TestBackfillAsksTheNextSiteWhenOneHasNothing(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex, mangahub := sourceIDOf(t, db, "mangadex"), sourceIDOf(t, db, "mangahub")
	const primaryURL, alternateURL = "https://mangadex.org/title/two-sites", "https://mangahub.io/manga/two-sites"
	tracker := insertTracker(t, db, "two sites", mangadex, primaryURL)
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?)`, tracker, mangahub, alternateURL); err != nil {
		t.Fatal(err)
	}

	silent := &askedConnector{key: "mangadex"}
	knows := &askedConnector{key: "mangahub", answer: &connectors.SeriesMetadata{Type: "manhwa"}}
	service := NewService(repository.NewMetadataRepository(db), fakeResolver{"mangadex": silent, "mangahub": knows},
		Config{Backfill: true, BackfillGap: time.Millisecond, BackfillRetryAfter: time.Hour}, nil)

	if asked := service.backfillPass(ctx); asked != 1 || len(silent.asked) != 1 || len(knows.asked) != 0 {
		t.Fatalf("first pass asked %d sites (mangadex %d, mangahub %d); want the better-ranked site, once", asked, len(silent.asked), len(knows.asked))
	}
	if asked := service.backfillPass(ctx); asked != 1 || len(silent.asked) != 1 || len(knows.asked) != 1 {
		t.Fatalf("second pass asked %d sites (mangadex %d, mangahub %d); want the tracker's other site", asked, len(silent.asked), len(knows.asked))
	}
	if written, err := service.Reconcile(ctx); err != nil || written != 1 {
		t.Fatalf("Reconcile = %d, %v; want the tracker's view built from its second site", written, err)
	}
	// With metadata stored the tracker is no longer the backfill's business.
	if asked := service.backfillPass(ctx); asked != 0 {
		t.Fatalf("a tracker with metadata was asked about again (%d sites)", asked)
	}
}

// TestBackfillDoesNotHoldALocalRefusalAgainstTheLink: a resolve refused
// because the site is cooling down, or our own queue for it is full, asked
// nobody. Marked as a failure, a ten-minute cooldown cost the link its whole
// retry window, twelve hours by default.
func TestBackfillDoesNotHoldALocalRefusalAgainstTheLink(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex := sourceIDOf(t, db, "mangadex")
	insertTracker(t, db, "refused", mangadex, "https://mangadex.org/title/refused")

	connector := &askedConnector{
		key:      "mangadex",
		failWith: fmt.Errorf("resolve: %w", &connectors.SourceCoolingDownError{Host: "api.mangadex.org", RetryAfter: 8 * time.Minute}),
	}
	service := NewService(repository.NewMetadataRepository(db), fakeResolver{"mangadex": connector},
		Config{Backfill: true, BackfillGap: time.Millisecond, BackfillRetryAfter: time.Hour}, nil)

	service.backfillPass(ctx)
	if len(service.backfillFailed) != 0 {
		t.Fatalf("a local refusal was remembered as a failure: %v", service.backfillFailed)
	}
	connector.failWith, connector.answer = nil, &connectors.SeriesMetadata{Type: "manga"}
	service.backfillPass(ctx)
	if len(connector.asked) != 2 {
		t.Fatalf("after the refusal the site was asked %d times in all, want 2", len(connector.asked))
	}
	if written, err := service.Reconcile(ctx); err != nil || written != 1 {
		t.Fatalf("Reconcile = %d, %v; want the view the second pass brought", written, err)
	}
}

// TestBackfillForgetsFailuresPastTheirWindow: nothing used to remove an entry
// from the failure memory — not the window passing, not the tracker being
// deleted — so it only ever grew.
func TestBackfillForgetsFailuresPastTheirWindow(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex := sourceIDOf(t, db, "mangadex")
	gone := insertTracker(t, db, "gone", mangadex, "https://mangadex.org/title/gone")
	insertTracker(t, db, "stays", mangadex, "https://mangadex.org/title/stays")

	silent := &askedConnector{key: "mangadex"}
	service := NewService(repository.NewMetadataRepository(db), fakeResolver{"mangadex": silent},
		Config{Backfill: true, BackfillGap: time.Millisecond, BackfillRetryAfter: time.Hour}, nil)
	clock := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }

	service.backfillPass(ctx)
	if len(service.backfillFailed) != 2 {
		t.Fatalf("precondition: both links failed and are remembered, got %v", service.backfillFailed)
	}
	if _, err := db.Exec(`DELETE FROM trackers WHERE id = ?`, gone); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Hour)
	service.backfillPass(ctx)
	if len(service.backfillFailed) != 1 {
		t.Fatalf("past the window the memory holds %d entries, want only the link that failed again: %v",
			len(service.backfillFailed), service.backfillFailed)
	}
	for key := range service.backfillFailed {
		if key.trackerID == gone {
			t.Fatalf("a deleted tracker's failure is still remembered: %+v", key)
		}
	}
}

// TestBackfillScheduleBacksOffWhileIdle pins the pacing the loop gives the
// backfill: a pass per reconcile tick while there is work, twice as far apart
// after each pass that asked nobody, and back at once when one does. It ran
// some eighty times an hour whatever it found, which after the first days is
// nothing.
func TestBackfillScheduleBacksOffWhileIdle(t *testing.T) {
	const tick = 3 * time.Minute
	schedule := backfillSchedule{base: tick}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	// passesIn walks the ticks of the given span and returns the offsets at
	// which a pass ran, each pass asking the given number of sites.
	passesIn := func(span time.Duration, asked int) []time.Duration {
		var ran []time.Duration
		start := now
		for ; now.Sub(start) < span; now = now.Add(tick) {
			if schedule.due(now) {
				ran = append(ran, now.Sub(start))
				schedule.ran(asked, now)
			}
		}
		return ran
	}
	minutes := func(offsets []time.Duration) string {
		parts := make([]string, 0, len(offsets))
		for _, offset := range offsets {
			parts = append(parts, fmt.Sprint(int(offset.Minutes())))
		}
		return strings.Join(parts, ",")
	}

	if got := minutes(passesIn(12*time.Minute, 3)); got != "0,3,6,9" {
		t.Fatalf("with work to do the passes ran at minutes %s, want every tick", got)
	}
	// Idle from here: 6, 12, 24 and then 30 minutes apart.
	if got := minutes(passesIn(3*time.Hour, 0)); got != "0,6,18,42,72,102,132,162" {
		t.Fatalf("idle, the passes ran at minutes %s, want them to back off to one every half hour", got)
	}
	if got := len(passesIn(time.Hour, 0)); got != 2 {
		t.Fatalf("an idle hour ran %d passes, want 2", got)
	}

	// One pass with something to ask and it is back on every tick; a pass
	// that stopped at its bound is due again at the very next one.
	for !schedule.due(now) {
		now = now.Add(tick)
	}
	schedule.ran(1, now)
	now = now.Add(tick)
	if !schedule.due(now) {
		t.Fatal("after a pass that asked a site the next tick must run one")
	}
	schedule.ran(maxBackfillPerPass, now)
	if !schedule.due(now) {
		t.Fatal("a pass that stopped at its bound has more to do and must be due at once")
	}
}

// TestReconcileRebuildsOnlyTheTrackerWhoseSourceRowWasRefreshed: a refreshed
// source row must still be noticed — now from its stamp alone — and the
// payloads read, and the view rewritten, for that tracker and no other.
func TestReconcileRebuildsOnlyTheTrackerWhoseSourceRowWasRefreshed(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex, mangafire := sourceIDOf(t, db, "mangadex"), sourceIDOf(t, db, "mangafire")
	const refreshedURL, quietURL = "https://mangadex.org/title/refreshed", "https://mangadex.org/title/quiet"
	const alternateURL = "https://mangafire.to/title/refreshed.x1"
	refreshed := insertTracker(t, db, "refreshed", mangadex, refreshedURL)
	quiet := insertTracker(t, db, "quiet", mangadex, quietURL)
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?)`, refreshed, mangafire, alternateURL); err != nil {
		t.Fatal(err)
	}

	store := &meteredStore{Store: repository.NewMetadataRepository(db)}
	service := NewService(store, nil, Config{}, nil)
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }

	service.Record(ctx, mangadex, refreshedURL, &connectors.SeriesMetadata{Type: "manhwa", Status: "ongoing"})
	service.Record(ctx, mangafire, alternateURL, &connectors.SeriesMetadata{Genres: []string{"Action"}})
	service.Record(ctx, mangadex, quietURL, &connectors.SeriesMetadata{Type: "manga", Status: "ongoing"})
	if written, err := service.Reconcile(ctx); err != nil || written != 2 {
		t.Fatalf("first Reconcile = %d, %v; want both views built", written, err)
	}
	quietBuiltAt := viewUpdatedAt(t, db, quiet)

	// Past the refresh window the poller's next answer replaces the row.
	clock = clock.Add(RefreshAfter + time.Hour)
	service.Record(ctx, mangadex, refreshedURL, &connectors.SeriesMetadata{Type: "manhwa", Status: "completed"})
	store.reset()

	written, err := service.Reconcile(ctx)
	if err != nil || written != 1 {
		t.Fatalf("Reconcile after a refresh = %d, %v; want exactly one view rebuilt", written, err)
	}
	if len(store.payloadListings) != 1 || len(store.payloadListings[0]) != 1 || store.payloadListings[0][0] != refreshed {
		t.Fatalf("payloads were read for %v, want only tracker %d", store.payloadListings, refreshed)
	}
	if len(store.saved) != 1 || len(store.saved[0]) != 1 || store.saved[0][0] != refreshed {
		t.Fatalf("views were written for %v, want only tracker %d", store.saved, refreshed)
	}

	repo := repository.NewMetadataRepository(db)
	row, err := repo.GetTrackerMetadata(ctx, 1, refreshed)
	if err != nil || row == nil {
		t.Fatalf("view = %+v, %v", row, err)
	}
	// The rebuilt view is the whole tracker's again: the refreshed site's new
	// status and the untouched alternate's genres.
	if row.SeriesStatus != "completed" || row.SeriesType != "manhwa" || row.GenresJSON != `["Action"]` {
		t.Fatalf("rebuilt view = %+v, want the refreshed status with the alternate's genres kept", row)
	}
	if !viewUpdatedAt(t, db, quiet).Equal(quietBuiltAt) {
		t.Fatal("the tracker whose sources did not change had its view rewritten")
	}
	if quietRow, _ := repo.GetTrackerMetadata(ctx, 1, quiet); quietRow == nil || quietRow.SeriesStatus != "ongoing" {
		t.Fatalf("the quiet tracker's view = %+v", quietRow)
	}
}

// TestReconcileReadsNoPayloadsWhenNothingChanged is the steady state the
// loop spends nearly all its passes in: the signatures match, so no payload
// is read and nothing is written.
func TestReconcileReadsNoPayloadsWhenNothingChanged(t *testing.T) {
	db := openServiceTestDB(t)
	ctx := context.Background()
	mangadex := sourceIDOf(t, db, "mangadex")
	const withURL = "https://mangadex.org/title/with"
	with := insertTracker(t, db, "with", mangadex, withURL)
	insertTracker(t, db, "without", mangadex, "https://mangadex.org/title/without")

	store := &meteredStore{Store: repository.NewMetadataRepository(db)}
	service := NewService(store, nil, Config{}, nil)
	service.Record(ctx, mangadex, withURL, &connectors.SeriesMetadata{Type: "manga"})
	if written, err := service.Reconcile(ctx); err != nil || written != 1 {
		t.Fatalf("first Reconcile = %d, %v", written, err)
	}
	builtAt := viewUpdatedAt(t, db, with)
	store.reset()

	for pass := 0; pass < 3; pass++ {
		if written, err := service.Reconcile(ctx); err != nil || written != 0 {
			t.Fatalf("Reconcile with nothing changed = %d, %v", written, err)
		}
	}
	if store.stampListings != 3 {
		t.Fatalf("three passes ran %d stamp listings", store.stampListings)
	}
	if len(store.payloadListings) != 0 {
		t.Fatalf("payloads were read with nothing to rebuild: %v", store.payloadListings)
	}
	if len(store.saved) != 0 {
		t.Fatalf("views were written with nothing changed: %v", store.saved)
	}
	if !viewUpdatedAt(t, db, with).Equal(builtAt) {
		t.Fatal("an unchanged view was rewritten")
	}
	var views int
	if err := db.QueryRow(`SELECT COUNT(1) FROM tracker_metadata`).Scan(&views); err != nil || views != 1 {
		t.Fatalf("views = %d, %v; a tracker with no metadata must not get one", views, err)
	}
}
