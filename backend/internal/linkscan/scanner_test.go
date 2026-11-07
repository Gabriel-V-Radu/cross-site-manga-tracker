package linkscan

import (
	"context"
	"errors"
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
	decided      map[int64][]string
}

func newStubStore(targets ...repository.LinkScanTarget) *stubStore {
	return &stubStore{
		targets:      targets,
		stored:       map[int64][]repository.LinkSuggestion{},
		mergedTitles: map[int64][]string{},
		decided:      map[int64][]string{},
	}
}

func (s *stubStore) DecidedCandidateURLs(_ context.Context, trackerID int64, _ int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decided[trackerID], nil
}

func (s *stubStore) ListScanTargets(context.Context, int64, int64, repository.LinkScanFilter) ([]repository.LinkScanTarget, error) {
	return s.targets, nil
}

func (s *stubStore) ReplacePendingSuggestions(_ context.Context, trackerID int64, _ int64, suggestions []repository.LinkSuggestion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored[trackerID] = suggestions
	return nil
}

func (s *stubStore) MergeRelatedTitles(_ context.Context, trackerID int64, titles []string) error {
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

func runScan(t *testing.T, store *stubStore, connector connectors.Connector, aid AidLookup) *Scanner {
	t.Helper()
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}
	scanner := NewScanner(store, registry, aid, slog.Default())
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	waitForScanEnd(t, scanner, 1)
	return scanner
}

func waitForScanEnd(t *testing.T, scanner *Scanner, profileID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for scanner.StatusFor(profileID).Progress.Running {
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// blockingConnector parks inside SearchByTitle until its context is cancelled,
// standing in for a scan caught mid-request when the process shuts down.
type blockingConnector struct {
	key     string
	entered chan struct{}
	once    sync.Once
}

func (c *blockingConnector) Key() string                       { return c.key }
func (c *blockingConnector) Name() string                      { return c.key }
func (c *blockingConnector) Kind() string                      { return connectors.KindNative }
func (c *blockingConnector) HealthCheck(context.Context) error { return nil }
func (c *blockingConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, fmt.Errorf("not found")
}
func (c *blockingConnector) SearchByTitle(ctx context.Context, _ string, _ int) ([]connectors.MangaResult, error) {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestCloseCancelsARunningScan covers shutdown: the scan goroutine must end,
// and the tracker it was halfway through must keep whatever candidates it
// already had rather than being handed the cancelled search's empty result.
func TestCloseCancelsARunningScan(t *testing.T) {
	connector := &blockingConnector{key: "weebcentral", entered: make(chan struct{})}
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	store := newStubStore(
		repository.LinkScanTarget{TrackerID: 1, Title: "First"},
		repository.LinkScanTarget{TrackerID: 2, Title: "Second"},
	)
	scanner := NewScanner(store, registry, nil, slog.Default())
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}

	<-connector.entered
	scanner.Close()

	if scanner.StatusFor(1).Progress.Running {
		t.Fatal("the scan goroutine outlived Close")
	}
	store.mu.Lock()
	stored := len(store.stored)
	store.mu.Unlock()
	if stored != 0 {
		t.Fatalf("a cancelled scan wrote %d tracker(s); it must store nothing it did not search", stored)
	}

	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err == nil {
		t.Fatal("a scan started after shutdown")
	}
}

// TestStopReportsStoppingUntilTheScanEnds pins the feedback Stop gives while
// the tracker in flight finishes, and that a Start in that window is refused
// with an error the dashboard can tell apart from a real failure.
func TestStopReportsStoppingUntilTheScanEnds(t *testing.T) {
	connector := &blockingConnector{key: "weebcentral", entered: make(chan struct{})}
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	store := newStubStore(repository.LinkScanTarget{TrackerID: 1, Title: "First"})
	scanner := NewScanner(store, registry, nil, slog.Default())
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	<-connector.entered

	if scanner.StatusFor(1).Progress.Stopping {
		t.Fatal("a scan nobody asked to stop reports Stopping")
	}
	if !scanner.Stop(1) {
		t.Fatal("Stop found no scan of the profile that started it")
	}
	progress := scanner.StatusFor(1).Progress
	if !progress.Running || !progress.Stopping {
		t.Fatalf("after Stop mid-tracker: running=%v stopping=%v, want both true", progress.Running, progress.Stopping)
	}

	err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{})
	if !errors.Is(err, ErrScanRunning) {
		t.Fatalf("start during wind-down = %v, want ErrScanRunning", err)
	}

	scanner.Close()
	progress = scanner.StatusFor(1).Progress
	if progress.Running || progress.Stopping || !progress.Stopped {
		t.Fatalf("after the scan ended: running=%v stopping=%v stopped=%v, want only stopped", progress.Running, progress.Stopping, progress.Stopped)
	}
}

// TestScanBelongsToTheProfileThatStartedIt pins the scanner's per-profile
// view. A second profile used to see the first one's running scan with a Stop
// that ended it, and afterwards that scan's result as its own "Last scan".
func TestScanBelongsToTheProfileThatStartedIt(t *testing.T) {
	connector := &blockingConnector{key: "weebcentral", entered: make(chan struct{})}
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	store := newStubStore(repository.LinkScanTarget{TrackerID: 1, Title: "First"})
	scanner := NewScanner(store, registry, nil, slog.Default())
	defer scanner.Close()
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	<-connector.entered

	other := scanner.StatusFor(2)
	if !other.BusyElsewhere {
		t.Fatal("another profile is not told that a scan holds the scanner")
	}
	if other.Progress.Running || other.Progress.SourceName != "" {
		t.Fatalf("another profile sees the running scan as its own: %+v", other.Progress)
	}
	if scanner.Stop(2) {
		t.Fatal("another profile's Stop reported stopping a scan it did not start")
	}
	if own := scanner.StatusFor(1); own.BusyElsewhere || !own.Progress.Running || own.Progress.Stopping {
		t.Fatalf("after another profile's Stop the owner sees %+v, want its scan running and not stopping", own)
	}

	if err := scanner.Start(2, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); !errors.Is(err, ErrScanRunningElsewhere) {
		t.Fatalf("start by another profile = %v, want ErrScanRunningElsewhere", err)
	}
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("second start by the owner = %v, want ErrScanRunning", err)
	}

	scanner.Close()
	if own := scanner.StatusFor(1).Progress; own.Running || own.FinishedAt == nil || own.ProfileID != 1 {
		t.Fatalf("the owner's finished scan = %+v, want it finished and its own", own)
	}
	if other := scanner.StatusFor(2); other.BusyElsewhere || other.Progress.SourceName != "" || other.Progress.FinishedAt != nil {
		t.Fatalf("another profile inherited the finished scan: %+v", other)
	}
}

// TestEachProfileKeepsItsOwnLastScan: a later scan by another profile must not
// take over the earlier profile's "Last scan" line.
func TestEachProfileKeepsItsOwnLastScan(t *testing.T) {
	connector := &stubConnector{
		key: "weebcentral",
		searchResults: map[string][]connectors.MangaResult{
			"Nano Machine": {{Title: "Nano Machine", URL: "https://weebcentral.com/series/01Y"}},
		},
	}
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}
	store := newStubStore(repository.LinkScanTarget{TrackerID: 3, Title: "Nano Machine"})
	scanner := NewScanner(store, registry, nil, slog.Default())
	defer scanner.Close()

	if err := scanner.Start(1, 42, connector.Key(), "WeebCentral", repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start profile 1 scan: %v", err)
	}
	waitForScanEnd(t, scanner, 1)

	store.mu.Lock()
	store.targets = nil
	store.mu.Unlock()
	if err := scanner.Start(2, 43, connector.Key(), "MangaHub", repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start profile 2 scan: %v", err)
	}
	waitForScanEnd(t, scanner, 2)

	first := scanner.StatusFor(1).Progress
	if first.ProfileID != 1 || first.SourceName != "WeebCentral" || first.Total != 1 || first.WithCandidates != 1 {
		t.Fatalf("profile 1's last scan = %+v, want its own WeebCentral scan", first)
	}
	second := scanner.StatusFor(2).Progress
	if second.ProfileID != 2 || second.SourceName != "MangaHub" || second.Total != 0 {
		t.Fatalf("profile 2's last scan = %+v, want its own MangaHub scan", second)
	}
}

// gatedStore holds ListScanTargets until released, standing in for the moment
// between Start and the trackers being counted.
type gatedStore struct {
	*stubStore
	release chan struct{}
}

func (g *gatedStore) ListScanTargets(ctx context.Context, profileID int64, sourceID int64, filter repository.LinkScanFilter) ([]repository.LinkScanTarget, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.stubStore.ListScanTargets(ctx, profileID, sourceID, filter)
}

// TestScanReportsStartingUntilItHasCountedItsTrackers pins the starting
// state: before the targets are counted the total is unknown, and the status
// line used to read "0/0 series checked" for a scan about to check dozens.
func TestScanReportsStartingUntilItHasCountedItsTrackers(t *testing.T) {
	connector := &stubConnector{key: "weebcentral"}
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}
	store := &gatedStore{
		stubStore: newStubStore(
			repository.LinkScanTarget{TrackerID: 1, Title: "First"},
			repository.LinkScanTarget{TrackerID: 2, Title: "Second"},
		),
		release: make(chan struct{}),
	}
	scanner := NewScanner(store, registry, nil, slog.Default())
	defer scanner.Close()
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}

	if progress := scanner.StatusFor(1).Progress; !progress.Running || !progress.Starting {
		t.Fatalf("before the targets are counted: %+v, want running and starting", progress)
	}
	close(store.release)
	waitForScanEnd(t, scanner, 1)

	if progress := scanner.StatusFor(1).Progress; progress.Starting || progress.Total != 2 || progress.Done != 2 {
		t.Fatalf("after the scan: %+v, want no longer starting with 2/2 checked", progress)
	}
}

// TestScanSkipsDecidedCandidatesBeforeRanking covers a tracker whose top hits
// the user already rejected: the next-best hit must take their place, and a
// tracker left with nothing new must not count as "with candidates".
func TestScanSkipsDecidedCandidatesBeforeRanking(t *testing.T) {
	connector := &stubConnector{
		key: "weebcentral",
		searchResults: map[string][]connectors.MangaResult{
			"Solo Leveling": {
				{Title: "Solo Leveling", URL: "https://weebcentral.com/series/a"},
				{Title: "Solo Leveling Ragnarok", URL: "https://weebcentral.com/series/b"},
				{Title: "Solo Leveling Side Story", URL: "https://weebcentral.com/series/c"},
				{Title: "Solo Leveling Arise From the Shadow", URL: "https://weebcentral.com/series/d"},
			},
			"Omniscient Reader": {
				{Title: "Omniscient Reader", URL: "https://weebcentral.com/series/e"},
			},
		},
	}
	store := newStubStore(
		repository.LinkScanTarget{TrackerID: 1, Title: "Solo Leveling"},
		repository.LinkScanTarget{TrackerID: 2, Title: "Omniscient Reader"},
	)
	store.decided[1] = []string{
		"https://weebcentral.com/series/a",
		"https://weebcentral.com/series/b",
		"https://weebcentral.com/series/c",
	}
	store.decided[2] = []string{"https://weebcentral.com/series/e"}

	scanner := runScan(t, store, connector, nil)

	suggestions := store.stored[1]
	if len(suggestions) != 1 || suggestions[0].CandidateURL != "https://weebcentral.com/series/d" {
		t.Fatalf("suggestions = %+v, want only the undecided fourth hit", suggestions)
	}
	if len(store.stored[2]) != 0 {
		t.Fatalf("a decided candidate came back: %+v", store.stored[2])
	}
	if got := scanner.StatusFor(1).Progress.WithCandidates; got != 1 {
		t.Fatalf("WithCandidates = %d, want 1: only the first tracker has anything new", got)
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
