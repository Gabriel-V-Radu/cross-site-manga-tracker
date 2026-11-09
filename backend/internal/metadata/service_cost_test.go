package metadata

import (
	"context"
	"database/sql"
	"errors"
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
