package linkscan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type stubStore struct {
	mu           sync.Mutex
	targets      []repository.LinkScanTarget
	stored       map[int64][]repository.LinkSuggestion
	mergedTitles map[int64][]string
}

func newStubStore(targets ...repository.LinkScanTarget) *stubStore {
	return &stubStore{
		targets:      targets,
		stored:       map[int64][]repository.LinkSuggestion{},
		mergedTitles: map[int64][]string{},
	}
}

func (s *stubStore) ListScanTargets(int64, int64) ([]repository.LinkScanTarget, error) {
	return s.targets, nil
}

func (s *stubStore) ReplacePendingSuggestions(trackerID int64, _ int64, suggestions []repository.LinkSuggestion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored[trackerID] = suggestions
	return nil
}

func (s *stubStore) MergeRelatedTitles(trackerID int64, titles []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergedTitles[trackerID] = append(s.mergedTitles[trackerID], titles...)
	return nil
}

type stubConnector struct {
	key           string
	searchResults map[string][]connectors.MangaResult
	resolved      map[string]*connectors.MangaResult
	searchCalls   []string
	resolveCalls  []string
}

func (c *stubConnector) Key() string                       { return c.key }
func (c *stubConnector) Name() string                      { return c.key }
func (c *stubConnector) Kind() string                      { return connectors.KindNative }
func (c *stubConnector) HealthCheck(context.Context) error { return nil }
func (c *stubConnector) ResolveByURL(_ context.Context, rawURL string) (*connectors.MangaResult, error) {
	c.resolveCalls = append(c.resolveCalls, rawURL)
	if result, ok := c.resolved[rawURL]; ok {
		return result, nil
	}
	return nil, fmt.Errorf("not found")
}
func (c *stubConnector) SearchByTitle(_ context.Context, title string, _ int) ([]connectors.MangaResult, error) {
	c.searchCalls = append(c.searchCalls, title)
	for query, results := range c.searchResults {
		if strings.EqualFold(query, title) {
			return results, nil
		}
	}
	return nil, nil
}

type stubAid struct {
	series []mangabaka.Series
	err    error
}

func (a *stubAid) Search(context.Context, string, int) ([]mangabaka.Series, error) {
	return a.series, a.err
}

func runScan(t *testing.T, store *stubStore, connector connectors.Connector, aid AidLookup) {
	t.Helper()
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}
	scanner := NewScanner(store, registry, aid, slog.Default())
	if err := scanner.Start(1, 42, connector.Key(), connector.Name()); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for scanner.Snapshot().Running {
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestScanUsesAggregatorIDForMangaUpdates pins the id path: with a confirmed
// aggregator record, the MangaUpdates scan resolves the exact series instead
// of fuzzy-searching, and the result is a single exact suggestion.
func TestScanUsesAggregatorIDForMangaUpdates(t *testing.T) {
	chapter := 325.0
	muURL := "https://www.mangaupdates.com/series/01w7hvo"
	connector := &stubConnector{
		key: "mangaupdates",
		resolved: map[string]*connectors.MangaResult{
			muURL: {SourceKey: "mangaupdates", SourceItemID: "114563652", Title: "Nano Machine",
				URL: muURL + "/nano-machine", LatestChapter: &chapter},
		},
	}
	aid := &stubAid{series: []mangabaka.Series{{
		ID: 145, Title: "Nano Machine",
		Titles:         []string{"Nano Machine", "Nano Mashin"},
		MangaUpdatesID: "01w7hvo",
	}}}
	store := newStubStore(repository.LinkScanTarget{TrackerID: 7, Title: "Nano Machine"})

	runScan(t, store, connector, aid)

	suggestions := store.stored[7]
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want exactly one", suggestions)
	}
	if suggestions[0].Score != 1.0 || suggestions[0].CandidateTitle != "Nano Machine" {
		t.Fatalf("unexpected suggestion: %+v", suggestions[0])
	}
	if len(connector.searchCalls) != 0 {
		t.Fatalf("id path must not fuzzy-search, searched: %v", connector.searchCalls)
	}
	if len(store.mergedTitles[7]) == 0 {
		t.Fatal("aggregator titles were not persisted")
	}
}

// TestScanEnrichedTitlesMatchForeignCatalogName covers the case the fuzzy scan
// kept missing: the source catalogs the series under its Japanese name, the
// tracker holds the English one, and only the aggregator knows both.
func TestScanEnrichedTitlesMatchForeignCatalogName(t *testing.T) {
	connector := &stubConnector{
		key: "weebcentral",
		searchResults: map[string][]connectors.MangaResult{
			// The English-title query finds nothing; the enriched alternate does.
			"Kaoru Hana wa Rin to Saku": {{
				SourceKey: "weebcentral", SourceItemID: "01X", Title: "Kaoru Hana wa Rin to Saku",
				URL: "https://weebcentral.com/series/01X",
			}},
		},
	}
	aid := &stubAid{series: []mangabaka.Series{{
		ID: 1, Title: "The Fragrant Flower Blooms With Dignity",
		Titles: []string{"The Fragrant Flower Blooms With Dignity", "Kaoru Hana wa Rin to Saku"},
	}}}
	store := newStubStore(repository.LinkScanTarget{TrackerID: 9, Title: "The Fragrant Flower Blooms With Dignity"})

	runScan(t, store, connector, aid)

	suggestions := store.stored[9]
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want one", suggestions)
	}
	// The candidate only carries the Japanese name, but the enriched wanted
	// set makes it an exact match.
	if suggestions[0].Score != 1.0 {
		t.Fatalf("score = %v, want 1.0 via enriched titles", suggestions[0].Score)
	}
}

// TestScanIgnoresAggregatorWithoutExactMatch pins the conservatism: a record
// that merely resembles the tracker must contribute nothing.
func TestScanIgnoresAggregatorWithoutExactMatch(t *testing.T) {
	connector := &stubConnector{key: "weebcentral"}
	aid := &stubAid{series: []mangabaka.Series{{
		ID: 2, Title: "Nano Machine Returns",
		Titles:         []string{"Nano Machine Returns"},
		MangaUpdatesID: "wrong",
	}}}
	store := newStubStore(repository.LinkScanTarget{TrackerID: 5, Title: "Nano Machine"})

	runScan(t, store, connector, aid)

	if len(store.mergedTitles[5]) != 0 {
		t.Fatalf("near-match record leaked titles: %v", store.mergedTitles[5])
	}
	if len(store.stored[5]) != 0 {
		t.Fatalf("unexpected suggestions: %+v", store.stored[5])
	}
}

// TestScanSurvivesAggregatorFailure: MangaBaka being down must degrade to the
// plain fuzzy scan, never fail it.
func TestScanSurvivesAggregatorFailure(t *testing.T) {
	connector := &stubConnector{
		key: "weebcentral",
		searchResults: map[string][]connectors.MangaResult{
			"Nano Machine": {{
				SourceKey: "weebcentral", SourceItemID: "01Y", Title: "Nano Machine",
				URL: "https://weebcentral.com/series/01Y",
			}},
		},
	}
	aid := &stubAid{err: fmt.Errorf("mangabaka is down")}
	store := newStubStore(repository.LinkScanTarget{TrackerID: 3, Title: "Nano Machine"})

	runScan(t, store, connector, aid)

	suggestions := store.stored[3]
	if len(suggestions) != 1 || suggestions[0].Score != 1.0 {
		t.Fatalf("fuzzy fallback failed: %+v", suggestions)
	}
}
