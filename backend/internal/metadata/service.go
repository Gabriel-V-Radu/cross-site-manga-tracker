package metadata

import (
	"context"
	"encoding/json"
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

// Store is the repository surface the service needs.
type Store interface {
	SourceMetadataFetchedAt(ctx context.Context, sourceID int64, sourceURL string) (*time.Time, error)
	UpsertSourceMetadata(ctx context.Context, sourceID int64, sourceURL string, payload string, fetchedAt time.Time) error
	ListMetadataLinks(ctx context.Context) ([]repository.MetadataLink, error)
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

	mu             sync.Mutex
	backfillFailed map[int64]time.Time

	stopped chan struct{}
	started atomic.Bool
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
		backfillFailed: map[int64]time.Time{},
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
		for {
			if s.cfg.Backfill {
				s.backfillPass(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-reconcileTicker.C:
				s.reconcileLogged(ctx)
			case <-dirtyTicker.C:
				if s.dirty.Load() {
					s.reconcileLogged(ctx)
				}
			}
		}
	}()
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
// or refreshed changes it.
func signatureOf(links []repository.MetadataLink) string {
	parts := make([]string, 0, len(links))
	for _, link := range links {
		if link.Payload == "" || link.FetchedAt == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d|%s|%d", link.SourceID, strings.ToLower(strings.TrimSpace(link.SourceURL)), link.FetchedAt.Unix()))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// Reconcile rebuilds every tracker view whose inputs changed since it was
// built and reports how many it wrote. It is cheap when nothing changed: two
// queries and a comparison per tracker.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	s.dirty.Store(false)
	links, err := s.store.ListMetadataLinks(ctx)
	if err != nil {
		return 0, err
	}
	signatures, err := s.store.TrackerMetadataSignatures(ctx)
	if err != nil {
		return 0, err
	}

	byTracker, order := groupLinks(links)
	now := s.now().UTC()
	rows := make([]repository.TrackerMetadataRow, 0)
	for _, trackerID := range order {
		trackerLinks := byTracker[trackerID]
		signature := signatureOf(trackerLinks)
		stored, exists := signatures[trackerID]
		if exists && stored == signature {
			continue
		}
		if !exists && signature == "" {
			// Nothing known and nothing stored: nothing to write yet.
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
// and ComicK are left out on purpose: both sit behind Cloudflare and react to
// bursts, and the poller asks them on every rotation anyway, so a tracker
// linked only there gets its metadata from the poller within a cycle or two
// without the backfill adding to their load.
var backfillOrder = []string{"mangaupdates", "mangadex", "asuracomic", "flamecomics", "freewebnovel", "webtoons", "mangahub", "mgeko"}

// maxBackfillPerPass bounds one pass so the reconcile loop keeps its rhythm;
// the next pass picks up where this one stopped.
const maxBackfillPerPass = 40

// backfillPass resolves, one at a time, trackers that have no stored metadata
// from any of their sites, through the best backfill-eligible site they are
// linked to. After the first days it finds nothing to do: the poller keeps
// every tracker's rows fresh, and new trackers get theirs from it or from here
// within minutes.
func (s *Service) backfillPass(ctx context.Context) {
	if s.resolver == nil {
		return
	}
	links, err := s.store.ListMetadataLinks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Debug("metadata backfill could not list links", "error", err)
		}
		return
	}
	byTracker, order := groupLinks(links)
	now := s.now()
	done := 0
	for _, trackerID := range order {
		if ctx.Err() != nil || done >= maxBackfillPerPass {
			return
		}
		trackerLinks := byTracker[trackerID]
		if hasAnyMetadata(trackerLinks) || s.recentlyFailed(trackerID, now) {
			continue
		}
		link, connector, ok := s.pickBackfillLink(trackerLinks)
		if !ok {
			continue
		}
		done++
		resolveCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, err := connector.ResolveByURL(resolveCtx, link.SourceURL)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil || result == nil || result.Metadata.Empty() {
			s.markFailed(trackerID, now)
			s.logger.Debug("metadata backfill found nothing", "trackerId", trackerID, "sourceKey", link.SourceKey, "error", err)
		} else {
			s.Record(ctx, link.SourceID, link.SourceURL, result.Metadata)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.cfg.BackfillGap):
		}
	}
}

func hasAnyMetadata(links []repository.MetadataLink) bool {
	for _, link := range links {
		if link.Payload != "" {
			return true
		}
	}
	return false
}

func (s *Service) pickBackfillLink(links []repository.MetadataLink) (repository.MetadataLink, connectors.Connector, bool) {
	for _, key := range backfillOrder {
		for _, link := range links {
			if !strings.EqualFold(link.SourceKey, key) {
				continue
			}
			connector, ok := s.resolver.Get(link.SourceKey)
			if !ok {
				continue
			}
			if reporter, cooling := connector.(connectors.CooldownReporter); cooling {
				if remaining, _ := reporter.CooldownRemaining(); remaining > 0 {
					continue
				}
			}
			return link, connector, true
		}
	}
	return repository.MetadataLink{}, nil, false
}

func (s *Service) recentlyFailed(trackerID int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	failedAt, ok := s.backfillFailed[trackerID]
	return ok && now.Sub(failedAt) < s.cfg.BackfillRetryAfter
}

func (s *Service) markFailed(trackerID int64, now time.Time) {
	s.mu.Lock()
	s.backfillFailed[trackerID] = now
	s.mu.Unlock()
}
