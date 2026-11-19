package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// RefreshAfter is how old a site's stored metadata must be before the poller's
// next answer from that site replaces it. Types, genres and synopses do not
// change; a publication status does, rarely, and a week late is soon enough
// for what it drives (the info panel, and polling a finished series less
// often). Every other answer is dropped without touching the database.
const RefreshAfter = 7 * 24 * time.Hour

// Store is the repository surface the service needs. The three link listings
// differ in what they cost: the loop asks the two cheap ones every few
// minutes, and the one that carries payloads only for views it must rebuild.
type Store interface {
	SourceMetadataFetchedAt(ctx context.Context, sourceID int64, sourceURL string) (*time.Time, error)
	UpsertSourceMetadata(ctx context.Context, sourceID int64, sourceURL string, payload string, fetchedAt time.Time) error
	// ListMetadataLinkStamps: every tracker's links with when each site's
	// metadata was stored, no payloads.
	ListMetadataLinkStamps(ctx context.Context) ([]repository.MetadataLink, error)
	// ListMetadataLinks: the given trackers' links, payloads included.
	ListMetadataLinks(ctx context.Context, trackerIDs []int64) ([]repository.MetadataLink, error)
	// ListMetadataBackfillLinks: the links of trackers with no stored
	// metadata from any site, no payloads.
	ListMetadataBackfillLinks(ctx context.Context) ([]repository.MetadataLink, error)
	TrackerMetadataSignatures(ctx context.Context) (map[int64]string, error)
	SaveTrackerMetadata(ctx context.Context, rows []repository.TrackerMetadataRow) error
}

// Resolver resolves a series URL on its site, which is how the backfill asks
// for metadata the poller has not brought yet.
type Resolver interface {
	Get(key string) (connectors.Connector, bool)
}

// Config tunes the service. Zero values take the defaults.
type Config struct {
	// ReconcileEvery is how often every tracker's consolidated view is checked
	// against its inputs (links added or removed, a source row refreshed).
	ReconcileEvery time.Duration
	// Backfill turns on asking sites directly for trackers that have no
	// metadata at all yet. Off where the server must not reach the network
	// (QA servers run with polling disabled).
	Backfill bool
	// BackfillGap is the pause between two backfill resolves, on top of the
	// shared per-host throttle.
	BackfillGap time.Duration
	// BackfillRetryAfter is how long a tracker whose backfill failed is left
	// alone before it is tried again.
	BackfillRetryAfter time.Duration
}

// Service records the metadata the poller's resolves carry, keeps every
// tracker's consolidated view current, and backfills trackers the poller has
// not brought anything for yet.
type Service struct {
	store    Store
	resolver Resolver
	logger   *slog.Logger
	cfg      Config
	now      func() time.Time

	dirty atomic.Bool

	mu sync.Mutex
	// backfillFailed is when the backfill last came back empty-handed from
	// each link it asked, so that link is left alone for BackfillRetryAfter.
	// It is kept per link and not per tracker: per tracker, one site with
	// nothing to say about a series shut the tracker out for the whole window
	// while its other linked sites, which might have answered, were never
	// asked.
	backfillFailed map[backfillLinkKey]time.Time

	stopped chan struct{}
	started atomic.Bool
}

// backfillLinkKey names one link of one tracker the way the stored rows are
// matched: by site and LOWER(TRIM(url)).
type backfillLinkKey struct {
	trackerID int64
	sourceID  int64
	url       string
}

func backfillKeyOf(link repository.MetadataLink) backfillLinkKey {
	return backfillLinkKey{
		trackerID: link.TrackerID,
		sourceID:  link.SourceID,
		url:       strings.ToLower(strings.TrimSpace(link.SourceURL)),
	}
}

func NewService(store Store, resolver Resolver, cfg Config, logger *slog.Logger) *Service {
	if cfg.ReconcileEvery <= 0 {
		cfg.ReconcileEvery = 3 * time.Minute
	}
	if cfg.BackfillGap <= 0 {
		cfg.BackfillGap = 4 * time.Second
	}
	if cfg.BackfillRetryAfter <= 0 {
		cfg.BackfillRetryAfter = 12 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:          store,
		resolver:       resolver,
		logger:         logger,
		cfg:            cfg,
		now:            time.Now,
		backfillFailed: map[backfillLinkKey]time.Time{},
		stopped:        make(chan struct{}),
	}
}

// Record stores what a site just said about a series, unless the stored copy
// is younger than RefreshAfter. It is the poller's hook: called for every
// answered resolve, it costs one indexed read in the common case and never a
// request. Errors are logged, not returned: metadata must never fail a poll.
func (s *Service) Record(ctx context.Context, sourceID int64, sourceURL string, meta *connectors.SeriesMetadata) {
	if s == nil || meta.Empty() || sourceID <= 0 || strings.TrimSpace(sourceURL) == "" {
		return
	}
	now := s.now().UTC()
	fetchedAt, err := s.store.SourceMetadataFetchedAt(ctx, sourceID, sourceURL)
	if err != nil {
		s.logger.Debug("metadata age lookup failed", "sourceId", sourceID, "error", err)
		return
	}
	if fetchedAt != nil && now.Sub(*fetchedAt) < RefreshAfter {
		return
	}
	payload, err := json.Marshal(meta)
	if err != nil {
		return
	}
	if err := s.store.UpsertSourceMetadata(ctx, sourceID, sourceURL, string(payload), now); err != nil {
		s.logger.Warn("metadata store failed", "sourceId", sourceID, "error", err)
		return
	}
	s.dirty.Store(true)
}

// Start runs the reconcile loop (and the backfill when configured) until ctx
// ends. It reconciles once at once, so a fresh deploy builds what it can from
// rows already stored.
func (s *Service) Start(ctx context.Context) {
	s.started.Store(true)
	go func() {
		defer close(s.stopped)
		reconcileTicker := time.NewTicker(s.cfg.ReconcileEvery)
		defer reconcileTicker.Stop()
		// A dirty flag set by Record is picked up within a minute rather than
		// waiting for the next full tick, so a backfilled tracker shows its
		// info shortly after.
		dirtyTicker := time.NewTicker(time.Minute)
		defer dirtyTicker.Stop()

		s.reconcileLogged(ctx)
		backfill := backfillSchedule{base: s.cfg.ReconcileEvery}
		s.backfillIfDue(ctx, &backfill)
		for {
			select {
			case <-ctx.Done():
				return
			case <-reconcileTicker.C:
				s.reconcileLogged(ctx)
				// The backfill rides the reconcile tick and no other. It used
				// to run at the top of this loop, which the dirty ticker also
				// comes round to every minute whether or not anything is
				// dirty: some eighty listings an hour to learn, nearly every
				// time, that no tracker lacks metadata.
				s.backfillIfDue(ctx, &backfill)
			case <-dirtyTicker.C:
				if s.dirty.Load() {
					s.reconcileLogged(ctx)
				}
			}
		}
	}()
}

// backfillIdleMax is the longest the backfill waits between two passes while
// it keeps finding nobody to ask. A tracker added in that time is not left
// waiting for it: the poller's next cycle resolves a new tracker and brings
// its metadata along.
const backfillIdleMax = 30 * time.Minute

// backfillSchedule spaces the backfill's passes: every base while there is
// work, and twice as far apart after each pass that asked nobody, up to
// backfillIdleMax. One pass that asks somebody brings it back to base.
type backfillSchedule struct {
	base time.Duration
	wait time.Duration
	next time.Time
}

// ran records a pass, finished at now, that asked that many sites.
func (b *backfillSchedule) ran(asked int, now time.Time) {
	switch {
	case asked >= maxBackfillPerPass:
		// It stopped at its bound with more to do — a fresh library — and
		// goes on at the next tick.
		b.wait, b.next = b.base, now
		return
	case asked > 0 || b.wait <= 0:
		b.wait = b.base
	default:
		b.wait = min(b.wait*2, max(backfillIdleMax, b.base))
	}
	b.next = now.Add(b.wait)
}

// due reports whether the next pass may run at now. A tick that falls a
// moment short of the mark still counts: the passes are driven by a ticker
// of the same period, and a strict comparison would skip every other one.
func (b *backfillSchedule) due(now time.Time) bool {
	return !now.Before(b.next.Add(-b.base / 2))
}

func (s *Service) backfillIfDue(ctx context.Context, schedule *backfillSchedule) {
	if !s.cfg.Backfill || !schedule.due(time.Now()) {
		return
	}
	asked := s.backfillPass(ctx)
	schedule.ran(asked, time.Now())
}

// StopWait waits for the loop to end after its context was cancelled.
func (s *Service) StopWait(timeout time.Duration) {
	if s == nil || !s.started.Load() {
		return
	}
	select {
	case <-s.stopped:
	case <-time.After(timeout):
	}
}

func (s *Service) reconcileLogged(ctx context.Context) {
	if _, err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
		s.logger.Warn("metadata reconcile failed", "error", err)
	}
}

// trackerInputs groups the library's links by tracker.
func groupLinks(links []repository.MetadataLink) (map[int64][]repository.MetadataLink, []int64) {
	byTracker := map[int64][]repository.MetadataLink{}
	order := make([]int64, 0)
	for _, link := range links {
		if _, ok := byTracker[link.TrackerID]; !ok {
			order = append(order, link.TrackerID)
		}
		byTracker[link.TrackerID] = append(byTracker[link.TrackerID], link)
	}
	return byTracker, order
}

// signatureOf names what a tracker's view is built from: every linked site
// that has stored metadata, with when it was stored. Any link added, removed
// or refreshed changes it. It reads no payload — FetchedAt alone says whether
// a site has metadata — so it can be computed from the payload-free listing.
func signatureOf(links []repository.MetadataLink) string {
	parts := make([]string, 0, len(links))
	for _, link := range links {
		if link.FetchedAt == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d|%s|%d", link.SourceID, strings.ToLower(strings.TrimSpace(link.SourceURL)), link.FetchedAt.Unix()))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// viewIsStale reports whether a tracker's stored view has to be rebuilt:
// its inputs no longer match the signature it was built from, or it has
// inputs and no view yet.
func viewIsStale(signatures map[int64]string, trackerID int64, signature string) bool {
	stored, exists := signatures[trackerID]
	if exists {
		return stored != signature
	}
	// Nothing known and nothing stored: nothing to write yet.
	return signature != ""
}

// Reconcile rebuilds every tracker view whose inputs changed since it was
// built and reports how many it wrote. It is cheap when nothing changed: two
// queries that carry no payloads and a comparison per tracker. The payloads —
// nearly all of what the tables hold — are read only for the trackers whose
// view is stale, which between two passes is none or a handful.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	s.dirty.Store(false)
	stamps, err := s.store.ListMetadataLinkStamps(ctx)
	if err != nil {
		return 0, err
	}
	signatures, err := s.store.TrackerMetadataSignatures(ctx)
	if err != nil {
		return 0, err
	}

	byTracker, order := groupLinks(stamps)
	stale := make([]int64, 0)
	for _, trackerID := range order {
		if viewIsStale(signatures, trackerID, signatureOf(byTracker[trackerID])) {
			stale = append(stale, trackerID)
		}
	}
	if len(stale) == 0 {
		return 0, nil
	}

	links, err := s.store.ListMetadataLinks(ctx, stale)
	if err != nil {
		return 0, err
	}
	byTracker, order = groupLinks(links)
	now := s.now().UTC()
	rows := make([]repository.TrackerMetadataRow, 0, len(order))
	for _, trackerID := range order {
		trackerLinks := byTracker[trackerID]
		// The signature is taken again from the rows the view is built from,
		// not carried over from the stamps: the poller stores metadata while
		// this runs, and a view signed with an older stamp than the payload it
		// holds would be rebuilt once more for nothing. A tracker deleted in
		// between has no rows here and is simply not visited.
		signature := signatureOf(trackerLinks)
		if !viewIsStale(signatures, trackerID, signature) {
			continue
		}
		consolidated := Consolidate(entriesOf(trackerLinks))
		row, err := rowOf(trackerID, consolidated, signature, now)
		if err != nil {
			continue
		}
		rows = append(rows, row)
	}
	if err := s.store.SaveTrackerMetadata(ctx, rows); err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		s.logger.Info("metadata views rebuilt", "trackers", len(rows))
	}
	return len(rows), nil
}

func entriesOf(links []repository.MetadataLink) []SourceEntry {
	entries := make([]SourceEntry, 0, len(links))
	for _, link := range links {
		if link.Payload == "" {
			continue
		}
		var meta connectors.SeriesMetadata
		if err := json.Unmarshal([]byte(link.Payload), &meta); err != nil {
			continue
		}
		entries = append(entries, SourceEntry{
			SourceKey:  link.SourceKey,
			SourceName: link.SourceName,
			SourceURL:  link.SourceURL,
			Metadata:   meta,
		})
	}
	return entries
}

func rowOf(trackerID int64, consolidated Consolidated, signature string, now time.Time) (repository.TrackerMetadataRow, error) {
	payload, err := json.Marshal(consolidated)
	if err != nil {
		return repository.TrackerMetadataRow{}, err
	}
	genres := consolidated.Genres
	if genres == nil {
		genres = []string{}
	}
	genresJSON, err := json.Marshal(genres)
	if err != nil {
		return repository.TrackerMetadataRow{}, err
	}
	return repository.TrackerMetadataRow{
		TrackerID:    trackerID,
		SeriesType:   consolidated.Type,
		SeriesStatus: consolidated.Status,
		GenresJSON:   string(genresJSON),
		Payload:      string(payload),
		Signature:    signature,
		UpdatedAt:    now,
	}, nil
}

// backfillOrder is which linked site the backfill asks, best first. MangaFire
// and ComicK come last on purpose: both sit behind Cloudflare and react to
// bursts, so they are asked only for a tracker linked to nothing else — a
// handful in a whole library. Leaving them out entirely left those trackers to
// the poller, which visits a finished, quiet series only every few days.
var backfillOrder = []string{"mangaupdates", "mangadex", "asuracomic", "flamecomics", "freewebnovel", "webtoons", "mangahub", "mgeko", "mangafire", "comick"}

// maxBackfillPerPass bounds one pass so the reconcile loop keeps its rhythm;
// the next pass picks up where this one stopped.
const maxBackfillPerPass = 40

// backfillPass resolves, one at a time, trackers that have no stored metadata
// from any of their sites, through the best backfill-eligible site they are
// linked to. After the first days it finds nothing to do: the poller keeps
// every tracker's rows fresh, and new trackers get theirs from it or from here
// within minutes.
//
// A tracker gets one site asked per pass: the best of its linked sites that
// did not come back empty within BackfillRetryAfter. A site that has nothing
// for the series is therefore passed over next time and the following one
// asked, where it used to be the whole tracker that sat out the window.
//
// It reports how many sites it asked, which is what paces the passes
// (backfillSchedule).
func (s *Service) backfillPass(ctx context.Context) int {
	if s.resolver == nil {
		return 0
	}
	// Only the trackers with no metadata at all, and no payloads: it used to
	// read the whole library's stored metadata each time to find, almost
	// always, nobody to ask about.
	links, err := s.store.ListMetadataBackfillLinks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Debug("metadata backfill could not list links", "error", err)
		}
		return 0
	}
	byTracker, order := groupLinks(links)
	now := s.now()
	s.forgetOldFailures(now)
	asked := 0
	for _, trackerID := range order {
		if ctx.Err() != nil || asked >= maxBackfillPerPass {
			return asked
		}
		link, connector, ok := s.pickBackfillLink(byTracker[trackerID], now)
		if !ok {
			continue
		}
		asked++
		resolveCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, err := connector.ResolveByURL(resolveCtx, link.SourceURL)
		cancel()
		if ctx.Err() != nil {
			return asked
		}
		var refused *connectors.SourceCoolingDownError
		switch {
		case errors.As(err, &refused):
			// Refused locally — the site cooling down, our own queue full. It
			// was not asked, so there is nothing to hold against the link: a
			// ten-minute cooldown must not cost it the retry window.
			s.logger.Debug("metadata backfill not asked: refused locally", "trackerId", trackerID, "sourceKey", link.SourceKey, "error", err)
		case err != nil || result == nil || result.Metadata.Empty():
			s.markFailed(link, now)
			s.logger.Debug("metadata backfill found nothing", "trackerId", trackerID, "sourceKey", link.SourceKey, "error", err)
		default:
			s.Record(ctx, link.SourceID, link.SourceURL, result.Metadata)
		}
		select {
		case <-ctx.Done():
			return asked
		case <-time.After(s.cfg.BackfillGap):
		}
	}
	return asked
}

// pickBackfillLink is the best of a tracker's links the backfill can ask
// right now: by backfillOrder, leaving out a site with no connector, one that
// is cooling down, and a link that came back empty within the retry window.
func (s *Service) pickBackfillLink(links []repository.MetadataLink, now time.Time) (repository.MetadataLink, connectors.Connector, bool) {
	for _, key := range backfillOrder {
		for _, link := range links {
			if !strings.EqualFold(link.SourceKey, key) {
				continue
			}
			connector, ok := s.resolver.Get(link.SourceKey)
			if !ok {
				continue
			}
			// The connector's own breaker and the shared circuit of its hosts
			// alike: only two connectors have the first.
			if remaining, _ := connectors.CooldownOf(connector, time.Now()); remaining > 0 {
				continue
			}
			if s.recentlyFailed(link, now) {
				continue
			}
			return link, connector, true
		}
	}
	return repository.MetadataLink{}, nil, false
}

func (s *Service) recentlyFailed(link repository.MetadataLink, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	failedAt, ok := s.backfillFailed[backfillKeyOf(link)]
	return ok && now.Sub(failedAt) < s.cfg.BackfillRetryAfter
}

func (s *Service) markFailed(link repository.MetadataLink, now time.Time) {
	s.mu.Lock()
	s.backfillFailed[backfillKeyOf(link)] = now
	s.mu.Unlock()
}

// forgetOldFailures drops the failures whose retry window has passed. Nothing
// else ever removed an entry — not the window passing, not the tracker being
// deleted — so on a server that runs for months the map only grew.
func (s *Service) forgetOldFailures(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, failedAt := range s.backfillFailed {
		if now.Sub(failedAt) >= s.cfg.BackfillRetryAfter {
			delete(s.backfillFailed, key)
		}
	}
}
