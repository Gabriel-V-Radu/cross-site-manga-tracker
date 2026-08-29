// Package linkscan finds candidate links on a source for trackers that lack
// one, and stores them as suggestions for in-app review. It replaces the old
// flow of producing a CSV, reviewing it in a spreadsheet and feeding it to
// link-alternate-sources.
package linkscan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// minKeepScore is the floor below which a search hit is not even worth showing
// as a suggestion; maxCandidatesPerTracker keeps the review card readable.
const (
	minKeepScore            = 0.35
	maxCandidatesPerTracker = 3
	perRequestTimeout       = 15 * time.Second
)

// shutdownGrace is how long Close waits for a running scan to notice the
// cancelled context and return. It only has to cover unwinding an already
// aborted request, and the process is on its way out: waiting longer would
// hold up shutdown for nothing.
const shutdownGrace = 2 * time.Second

// Progress is a snapshot of the running (or last finished) scan, for the
// dashboard's polling indicator.
type Progress struct {
	Running    bool
	SourceID   int64
	SourceName string
	Total      int
	Done       int
	// WithCandidates counts scanned trackers that yielded at least one
	// suggestion; the rest land in the queue's "no candidate found" section.
	WithCandidates int
	StartedAt      time.Time
	FinishedAt     *time.Time
	LastError      string
	// Stopped records that the user cut the scan short; what was already
	// stored stays stored, the rest simply was not visited.
	Stopped bool
}

// suggestionStore takes the context-carrying repository methods: a scan writes
// from a background goroutine against a one-connection pool, so a statement
// left waiting for that connection while the process shuts down would hang
// with nothing to report it.
type suggestionStore interface {
	ListScanTargetsContext(ctx context.Context, profileID int64, sourceID int64, filter repository.LinkScanFilter) ([]repository.LinkScanTarget, error)
	ReplacePendingSuggestionsContext(ctx context.Context, trackerID int64, sourceID int64, suggestions []repository.LinkSuggestion) error
	MergeRelatedTitlesContext(ctx context.Context, trackerID int64, titles []string) error
}

// AidLookup is the metadata aggregator consulted per tracker before searching
// the source itself — in production, MangaBaka. A confirmed record contributes
// every alternate title the series has (better scoring, better queries) and
// the series' MangaUpdates id (an exact link instead of a fuzzy search).
type AidLookup interface {
	Search(ctx context.Context, query string, limit int) ([]mangabaka.Series, error)
}

type Scanner struct {
	store    suggestionStore
	registry *connectors.Registry
	aid      AidLookup
	logger   *slog.Logger

	// The scanner's own lifetime, not any request's. Every scan and every
	// request it makes hangs off this context, so Close cuts the whole scan
	// loose at once — without it a scan kept issuing requests and writing
	// rows while the process was already tearing the database down.
	lifetime context.Context
	shutdown context.CancelFunc

	mu            sync.Mutex
	progress      Progress
	stopRequested bool
	// done is closed when the current scan's goroutine returns, so Close can
	// wait for it instead of racing the caller's db.Close().
	done chan struct{}
}

func NewScanner(store suggestionStore, registry *connectors.Registry, aid AidLookup, logger *slog.Logger) *Scanner {
	if logger == nil {
		logger = slog.Default()
	}
	lifetime, shutdown := context.WithCancel(context.Background())
	return &Scanner{
		store:    store,
		registry: registry,
		aid:      aid,
		logger:   logger,
		lifetime: lifetime,
		shutdown: shutdown,
	}
}

// Close cancels any running scan and waits briefly for its goroutine to
// return. The app's shutdown hook calls it: a scan is not request-scoped, so
// nothing else would ever stop it, and the alternative is HTTP requests and
// database writes continuing against a process that is closing its database.
// Whatever the scan already stored stands. Idempotent.
func (s *Scanner) Close() {
	s.shutdown()

	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return
	}

	select {
	case <-done:
	case <-time.After(shutdownGrace):
		s.logger.Warn("link scan did not stop within the shutdown grace period")
	}
}

// Start launches a scan in the background. Only one scan runs at a time — the
// second caller gets an error rather than a queue, because a scan walks an
// entire library against one site and two of them would defeat the pacing.
func (s *Scanner) Start(profileID int64, sourceID int64, sourceKey string, sourceName string, filter repository.LinkScanFilter) error {
	connector, ok := s.registry.Get(sourceKey)
	if !ok {
		return fmt.Errorf("no connector registered for source %q", sourceKey)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress.Running {
		return fmt.Errorf("a link scan is already running")
	}
	if s.lifetime.Err() != nil {
		// The process is shutting down; a scan launched now would be cancelled
		// on its first request anyway.
		return fmt.Errorf("the scanner is shutting down")
	}
	s.progress = Progress{
		Running:    true,
		SourceID:   sourceID,
		SourceName: sourceName,
		StartedAt:  time.Now().UTC(),
	}
	s.stopRequested = false
	done := make(chan struct{})
	s.done = done

	go s.run(s.lifetime, done, profileID, sourceID, connector, filter)
	return nil
}

// Stop asks a running scan to wind down after the tracker it is on. Whatever
// it already stored stands — stopping is for "that's enough requests", not a
// rollback.
func (s *Scanner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress.Running {
		s.stopRequested = true
	}
}

func (s *Scanner) Snapshot() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress
}

func (s *Scanner) shouldStop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopRequested
}

func (s *Scanner) run(ctx context.Context, done chan struct{}, profileID int64, sourceID int64, connector connectors.Connector, filter repository.LinkScanFilter) {
	defer func() {
		s.mu.Lock()
		now := time.Now().UTC()
		s.progress.Running = false
		s.progress.FinishedAt = &now
		s.progress.Stopped = s.stopRequested
		s.mu.Unlock()
		close(done)
	}()

	targets, err := s.store.ListScanTargetsContext(ctx, profileID, sourceID, filter)
	if err != nil {
		s.setError(fmt.Sprintf("load scan targets: %v", err))
		return
	}

	s.mu.Lock()
	s.progress.Total = len(targets)
	s.mu.Unlock()

	for _, target := range targets {
		// The user's stop and the process's shutdown end the walk the same
		// way: no new work, and what is already stored stays stored.
		if s.shouldStop() || ctx.Err() != nil {
			return
		}

		suggestions := s.findCandidates(ctx, connector, target, sourceID)

		// A cancelled scan comes back from the searches above with nothing to
		// show for them, and storing that nothing would erase the candidates
		// this tracker already had. Shutdown must cost a scan, never data.
		if ctx.Err() != nil {
			return
		}

		if err := s.store.ReplacePendingSuggestionsContext(ctx, target.TrackerID, sourceID, suggestions); err != nil {
			s.logger.Warn("link scan store failed", "trackerId", target.TrackerID, "error", err)
			s.setError(fmt.Sprintf("store suggestions: %v", err))
		}

		s.mu.Lock()
		s.progress.Done++
		if len(suggestions) > 0 {
			s.progress.WithCandidates++
		}
		s.mu.Unlock()
	}
}

func (s *Scanner) setError(message string) {
	s.mu.Lock()
	s.progress.LastError = message
	s.mu.Unlock()
}

// findCandidates searches the source for one tracker and scores what comes
// back. A metadata-aggregator lookup runs first: a confirmed record widens
// the title set candidates are scored against, and on MangaUpdates it skips
// the fuzzy search entirely in favour of the record's exact series id.
func (s *Scanner) findCandidates(ctx context.Context, connector connectors.Connector, target repository.LinkScanTarget, sourceID int64) []repository.LinkSuggestion {
	wanted := append([]string{target.Title}, target.RelatedTitles...)

	if aid := s.lookupAid(ctx, target); aid != nil {
		wanted = searchutil.UniqueNonEmpty(append(wanted, aid.Titles...))
		// Names learned from a confirmed record outlive this scan: they make
		// every future scan and dashboard search match better.
		if err := s.store.MergeRelatedTitlesContext(ctx, target.TrackerID, aid.Titles); err != nil {
			s.logger.Warn("merge related titles failed", "trackerId", target.TrackerID, "error", err)
		}

		if connector.Key() == "mangaupdates" {
			if suggestion := s.directSuggestion(ctx, connector, target, sourceID, aid.MangaUpdatesURL()); suggestion != nil {
				return []repository.LinkSuggestion{*suggestion}
			}
		}
	}

	queries := []string{target.Title}
	// A couple of alternate-title queries are enough of a second chance:
	// every extra query multiplies a full-library scan's request count.
	for _, alternate := range searchutil.FilterEnglishAlphabetNames(wanted) {
		if len(queries) >= 3 {
			break
		}
		if searchutil.Normalize(alternate) == searchutil.Normalize(target.Title) {
			continue
		}
		queries = append(queries, alternate)
	}

	seen := map[string]struct{}{}
	best := make([]repository.LinkSuggestion, 0, maxCandidatesPerTracker)
	for attempt, query := range queries {
		if attempt > 0 && len(best) > 0 {
			break
		}

		results := s.search(ctx, connector, query)
		for _, result := range results {
			url := strings.TrimSpace(result.URL)
			if url == "" {
				continue
			}
			if _, dup := seen[url]; dup {
				continue
			}
			seen[url] = struct{}{}

			score := ScoreCandidate(result.Title, wanted)
			if score < minKeepScore {
				continue
			}

			suggestion := repository.LinkSuggestion{
				TrackerID:      target.TrackerID,
				SourceID:       sourceID,
				CandidateURL:   url,
				CandidateTitle: strings.TrimSpace(result.Title),
				Score:          score,
			}
			if itemID := strings.TrimSpace(result.SourceItemID); itemID != "" {
				suggestion.CandidateItemID = &itemID
			}
			if cover := strings.TrimSpace(result.CoverImageURL); cover != "" {
				suggestion.CandidateCoverURL = &cover
			}
			suggestion.CandidateLatestChapter = result.LatestChapter
			suggestion.CandidateReleaseAt = result.LastUpdatedAt

			best = insertRanked(best, suggestion, maxCandidatesPerTracker)
		}
	}

	// Resolve the top candidate so the review card can show its latest
	// chapter and release date next to the tracker's — that comparison is
	// what settles most ambiguous matches. Only the top one: resolving every
	// candidate would double or triple a full-library scan.
	if len(best) > 0 && best[0].CandidateLatestChapter == nil {
		if resolved := s.resolve(ctx, connector, best[0].CandidateURL); resolved != nil {
			best[0].CandidateLatestChapter = resolved.LatestChapter
			best[0].CandidateReleaseAt = resolved.LastUpdatedAt
			if best[0].CandidateCoverURL == nil {
				if cover := strings.TrimSpace(resolved.CoverImageURL); cover != "" {
					best[0].CandidateCoverURL = &cover
				}
			}
		}
	}

	return best
}

// lookupAid asks the aggregator about the tracker and returns its record only
// on an exact normalized title match — a wrong record would poison both the
// scoring set and the id link, so near matches are not good enough.
func (s *Scanner) lookupAid(parent context.Context, target repository.LinkScanTarget) *mangabaka.Series {
	if s.aid == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(parent, perRequestTimeout)
	defer cancel()

	results, err := s.aid.Search(ctx, target.Title, 8)
	if err != nil {
		s.logger.Debug("aggregator lookup failed", "title", target.Title, "error", err)
		return nil
	}

	trackerTitles := map[string]struct{}{}
	for _, title := range append([]string{target.Title}, target.RelatedTitles...) {
		if normalized := searchutil.Normalize(title); normalized != "" {
			trackerTitles[normalized] = struct{}{}
		}
	}

	for index, series := range results {
		for _, title := range series.Titles {
			if _, ok := trackerTitles[searchutil.Normalize(title)]; ok {
				return &results[index]
			}
		}
	}
	return nil
}

// directSuggestion resolves a known series URL on the source and wraps it as
// the tracker's single, exact suggestion. Returns nil when there is no URL or
// the source cannot confirm it, letting the caller fall back to searching.
func (s *Scanner) directSuggestion(ctx context.Context, connector connectors.Connector, target repository.LinkScanTarget, sourceID int64, url string) *repository.LinkSuggestion {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	resolved := s.resolve(ctx, connector, url)
	if resolved == nil {
		return nil
	}

	suggestion := repository.LinkSuggestion{
		TrackerID:      target.TrackerID,
		SourceID:       sourceID,
		CandidateURL:   strings.TrimSpace(resolved.URL),
		CandidateTitle: strings.TrimSpace(resolved.Title),
		Score:          1.0,
	}
	if suggestion.CandidateURL == "" {
		suggestion.CandidateURL = url
	}
	if itemID := strings.TrimSpace(resolved.SourceItemID); itemID != "" {
		suggestion.CandidateItemID = &itemID
	}
	if cover := strings.TrimSpace(resolved.CoverImageURL); cover != "" {
		suggestion.CandidateCoverURL = &cover
	}
	suggestion.CandidateLatestChapter = resolved.LatestChapter
	suggestion.CandidateReleaseAt = resolved.LastUpdatedAt
	return &suggestion
}

func (s *Scanner) search(parent context.Context, connector connectors.Connector, query string) []connectors.MangaResult {
	ctx, cancel := context.WithTimeout(parent, perRequestTimeout)
	defer cancel()

	results, err := connector.SearchByTitle(ctx, query, 8)
	if err != nil {
		s.logger.Debug("link scan search failed", "query", query, "error", err)
		return nil
	}
	return results
}

func (s *Scanner) resolve(parent context.Context, connector connectors.Connector, url string) *connectors.MangaResult {
	ctx, cancel := context.WithTimeout(parent, perRequestTimeout)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, url)
	if err != nil {
		s.logger.Debug("link scan resolve failed", "url", url, "error", err)
		return nil
	}
	return result
}

// insertRanked keeps the slice sorted by score descending, capped at max.
func insertRanked(items []repository.LinkSuggestion, item repository.LinkSuggestion, max int) []repository.LinkSuggestion {
	position := len(items)
	for index, existing := range items {
		if item.Score > existing.Score {
			position = index
			break
		}
	}
	if position >= max {
		return items
	}

	items = append(items, repository.LinkSuggestion{})
	copy(items[position+1:], items[position:])
	items[position] = item
	if len(items) > max {
		items = items[:max]
	}
	return items
}
