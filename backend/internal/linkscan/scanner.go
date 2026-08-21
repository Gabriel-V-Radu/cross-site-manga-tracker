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
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// minKeepScore is the floor below which a search hit is not even worth showing
// as a suggestion; maxCandidatesPerTracker keeps the review card readable.
const (
	minKeepScore            = 0.35
	maxCandidatesPerTracker = 3
	perRequestTimeout       = 15 * time.Second
)

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
}

type suggestionStore interface {
	ListScanTargets(profileID int64, sourceID int64) ([]repository.LinkScanTarget, error)
	ReplacePendingSuggestions(trackerID int64, sourceID int64, suggestions []repository.LinkSuggestion) error
}

type Scanner struct {
	store    suggestionStore
	registry *connectors.Registry
	logger   *slog.Logger

	mu       sync.Mutex
	progress Progress
}

func NewScanner(store suggestionStore, registry *connectors.Registry, logger *slog.Logger) *Scanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scanner{store: store, registry: registry, logger: logger}
}

// Start launches a scan in the background. Only one scan runs at a time — the
// second caller gets an error rather than a queue, because a scan walks an
// entire library against one site and two of them would defeat the pacing.
func (s *Scanner) Start(profileID int64, sourceID int64, sourceKey string, sourceName string) error {
	connector, ok := s.registry.Get(sourceKey)
	if !ok {
		return fmt.Errorf("no connector registered for source %q", sourceKey)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress.Running {
		return fmt.Errorf("a link scan is already running")
	}
	s.progress = Progress{
		Running:    true,
		SourceID:   sourceID,
		SourceName: sourceName,
		StartedAt:  time.Now().UTC(),
	}

	go s.run(profileID, sourceID, connector)
	return nil
}

func (s *Scanner) Snapshot() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress
}

func (s *Scanner) run(profileID int64, sourceID int64, connector connectors.Connector) {
	defer func() {
		s.mu.Lock()
		now := time.Now().UTC()
		s.progress.Running = false
		s.progress.FinishedAt = &now
		s.mu.Unlock()
	}()

	targets, err := s.store.ListScanTargets(profileID, sourceID)
	if err != nil {
		s.setError(fmt.Sprintf("load scan targets: %v", err))
		return
	}

	s.mu.Lock()
	s.progress.Total = len(targets)
	s.mu.Unlock()

	for _, target := range targets {
		suggestions := s.findCandidates(connector, target, sourceID)

		if err := s.store.ReplacePendingSuggestions(target.TrackerID, sourceID, suggestions); err != nil {
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
// back. The tracker's title is tried first; its known alternate titles cover
// the case where the source catalogs the series under another language's name.
func (s *Scanner) findCandidates(connector connectors.Connector, target repository.LinkScanTarget, sourceID int64) []repository.LinkSuggestion {
	wanted := append([]string{target.Title}, target.RelatedTitles...)

	queries := []string{target.Title}
	// One alternate-title query is enough of a second chance: every extra
	// query multiplies a full-library scan's request count.
	if len(target.RelatedTitles) > 0 {
		queries = append(queries, target.RelatedTitles[0])
	}

	seen := map[string]struct{}{}
	best := make([]repository.LinkSuggestion, 0, maxCandidatesPerTracker)
	for attempt, query := range queries {
		if attempt > 0 && len(best) > 0 {
			break
		}

		results := s.search(connector, query)
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
		if resolved := s.resolve(connector, best[0].CandidateURL); resolved != nil {
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

func (s *Scanner) search(connector connectors.Connector, query string) []connectors.MangaResult {
	ctx, cancel := context.WithTimeout(context.Background(), perRequestTimeout)
	defer cancel()

	results, err := connector.SearchByTitle(ctx, query, 8)
	if err != nil {
		s.logger.Debug("link scan search failed", "query", query, "error", err)
		return nil
	}
	return results
}

func (s *Scanner) resolve(connector connectors.Connector, url string) *connectors.MangaResult {
	ctx, cancel := context.WithTimeout(context.Background(), perRequestTimeout)
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
