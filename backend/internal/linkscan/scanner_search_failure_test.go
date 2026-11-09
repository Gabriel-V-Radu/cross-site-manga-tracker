package linkscan

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// pendingStore is stubStore with the one behaviour these tests are about made
// visible: it records which trackers ReplacePendingSuggestions was called for,
// so "left untouched" can be told from "replaced with the same rows".
type pendingStore struct {
	*stubStore
	replaced map[int64]int
}

func newPendingStore(targets ...repository.LinkScanTarget) *pendingStore {
	return &pendingStore{stubStore: newStubStore(targets...), replaced: map[int64]int{}}
}

func (s *pendingStore) ReplacePendingSuggestions(ctx context.Context, trackerID int64, sourceID int64, suggestions []repository.LinkSuggestion) error {
	s.mu.Lock()
	s.replaced[trackerID]++
	s.mu.Unlock()
	return s.stubStore.ReplacePendingSuggestions(ctx, trackerID, sourceID, suggestions)
}

// seedPending gives a tracker the pending candidate an earlier scan found.
func (s *pendingStore) seedPending(trackerID int64, url string) {
	s.stored[trackerID] = []repository.LinkSuggestion{{
		TrackerID: trackerID, SourceID: 42, CandidateURL: url, CandidateTitle: "From an earlier scan", Score: 0.9,
	}}
}

// flakyConnector is stubConnector with searches that fail for chosen queries
// (or for all of them), the way a source that is cooling down or timing out
// answers.
type flakyConnector struct {
	*stubConnector
	failAll error
	failFor map[string]error
}

func (c *flakyConnector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	if c.failAll != nil {
		c.searchCalls = append(c.searchCalls, title)
		return nil, c.failAll
	}
	for query, err := range c.failFor {
		if strings.EqualFold(query, title) {
			c.searchCalls = append(c.searchCalls, title)
			return nil, err
		}
	}
	return c.stubConnector.SearchByTitle(ctx, title, limit)
}

// lockedBuffer lets the scan goroutine log while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runScanLogged(t *testing.T, store suggestionStore, connector connectors.Connector) (*Scanner, *lockedBuffer) {
	t.Helper()
	registry := connectors.NewRegistry()
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register connector: %v", err)
	}
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	scanner := NewScanner(store, registry, nil, logger)
	t.Cleanup(scanner.Close)
	if err := scanner.Start(1, 42, connector.Key(), connector.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	waitForScanEnd(t, scanner, 1)
	return scanner, logs
}

// TestScanKeepsPendingCandidatesWhenTheSearchFails is the rescan of a site
// that is cooling down: every search errors, and each tracker's review queue
// has to come out of the scan exactly as it went in. It used to come out
// empty — the failed search was read as "nothing found" and stored as such.
func TestScanKeepsPendingCandidatesWhenTheSearchFails(t *testing.T) {
	store := newPendingStore(
		repository.LinkScanTarget{TrackerID: 1, Title: "Solo Leveling"},
		repository.LinkScanTarget{TrackerID: 2, Title: "Omniscient Reader"},
	)
	store.seedPending(1, "https://weebcentral.com/series/a")
	store.seedPending(2, "https://weebcentral.com/series/e")
	connector := &flakyConnector{
		stubConnector: &stubConnector{key: "weebcentral"},
		failAll:       errors.New("weebcentral is cooling down for 5h59m"),
	}

	scanner, logs := runScanLogged(t, store, connector)

	if len(store.replaced) != 0 {
		t.Fatalf("a failed search replaced the stored candidates of trackers %v", store.replaced)
	}
	for trackerID, url := range map[int64]string{1: "https://weebcentral.com/series/a", 2: "https://weebcentral.com/series/e"} {
		if kept := store.stored[trackerID]; len(kept) != 1 || kept[0].CandidateURL != url {
			t.Fatalf("tracker %d lost its pending candidate to a failed search: %+v", trackerID, kept)
		}
	}

	progress := scanner.StatusFor(1).Progress
	if progress.Done != 2 || progress.Total != 2 {
		t.Fatalf("progress = %d/%d, want the failed trackers counted as checked", progress.Done, progress.Total)
	}
	if progress.SearchFailed != 2 || progress.WithCandidates != 0 {
		t.Fatalf("SearchFailed = %d, WithCandidates = %d; want 2 and 0", progress.SearchFailed, progress.WithCandidates)
	}
	if !strings.Contains(progress.LastError, "2 series could not be searched") || !strings.Contains(progress.LastError, "cooling down") {
		t.Fatalf("LastError = %q, want the count and the cause", progress.LastError)
	}

	logged := logs.String()
	if got := strings.Count(logged, "left stored candidates untouched"); got != 1 {
		t.Fatalf("the scan logged the failed searches %d times, want once for the whole scan:\n%s", got, logged)
	}
	if !strings.Contains(logged, "trackers=2") {
		t.Fatalf("the log line does not carry the count:\n%s", logged)
	}
}

// TestScanClearsPendingCandidatesWhenTheSearchFindsNothing is the other half:
// a search the source answered with nothing it can use is a real result, and
// the stale candidate an earlier scan left must go, as it always did.
func TestScanClearsPendingCandidatesWhenTheSearchFindsNothing(t *testing.T) {
	store := newPendingStore(repository.LinkScanTarget{TrackerID: 1, Title: "Solo Leveling"})
	store.seedPending(1, "https://weebcentral.com/series/a")
	connector := &flakyConnector{stubConnector: &stubConnector{key: "weebcentral"}}

	scanner, logs := runScanLogged(t, store, connector)

	if store.replaced[1] != 1 {
		t.Fatalf("an answered search replaced the candidates %d times, want once", store.replaced[1])
	}
	if left := store.stored[1]; len(left) != 0 {
		t.Fatalf("a stale candidate survived a search that found nothing: %+v", left)
	}
	progress := scanner.StatusFor(1).Progress
	if progress.Done != 1 || progress.SearchFailed != 0 || progress.LastError != "" {
		t.Fatalf("progress = %+v, want one tracker checked and nothing reported as failed", progress)
	}
	if logged := logs.String(); strings.Contains(logged, "left stored candidates untouched") {
		t.Fatalf("a scan with no failed search logged one:\n%s", logged)
	}
}

// TestScanTellsFailedSearchesFromEmptyOnesTrackerByTracker runs both through
// one scan, with a third tracker that finds a candidate, so the decision is
// shown to be each tracker's own and the counters to add up.
func TestScanTellsFailedSearchesFromEmptyOnesTrackerByTracker(t *testing.T) {
	store := newPendingStore(
		repository.LinkScanTarget{TrackerID: 1, Title: "Solo Leveling"},
		repository.LinkScanTarget{TrackerID: 2, Title: "Nano Machine"},
		repository.LinkScanTarget{TrackerID: 3, Title: "Omniscient Reader"},
	)
	store.seedPending(1, "https://weebcentral.com/series/a")
	store.seedPending(3, "https://weebcentral.com/series/e")
	connector := &flakyConnector{
		stubConnector: &stubConnector{
			key: "weebcentral",
			searchResults: map[string][]connectors.MangaResult{
				"Nano Machine": {{Title: "Nano Machine", URL: "https://weebcentral.com/series/01Y"}},
			},
		},
		failFor: map[string]error{"Solo Leveling": context.DeadlineExceeded},
	}

	scanner, _ := runScanLogged(t, store, connector)

	if kept := store.stored[1]; store.replaced[1] != 0 || len(kept) != 1 || kept[0].CandidateURL != "https://weebcentral.com/series/a" {
		t.Fatalf("the tracker whose search timed out was touched: replaced %d times, now %+v", store.replaced[1], kept)
	}
	if found := store.stored[2]; len(found) != 1 || found[0].CandidateURL != "https://weebcentral.com/series/01Y" {
		t.Fatalf("the tracker with a hit stored %+v", found)
	}
	if left := store.stored[3]; store.replaced[3] != 1 || len(left) != 0 {
		t.Fatalf("the tracker whose search found nothing kept %+v", left)
	}
	progress := scanner.StatusFor(1).Progress
	if progress.Done != 3 || progress.WithCandidates != 1 || progress.SearchFailed != 1 {
		t.Fatalf("progress = %+v, want 3 checked, 1 with candidates, 1 failed search", progress)
	}
	if !strings.Contains(progress.LastError, "1 series could not be searched") {
		t.Fatalf("LastError = %q", progress.LastError)
	}
}

// TestScanStoresCandidatesFoundDespiteAFailedSearch: the title query fails but
// the alternate-title query finds the series. What was found is stored — the
// guard is only for a tracker that would otherwise be handed nothing.
func TestScanStoresCandidatesFoundDespiteAFailedSearch(t *testing.T) {
	store := newPendingStore(repository.LinkScanTarget{
		TrackerID: 9, Title: "The Fragrant Flower Blooms With Dignity",
		RelatedTitles: []string{"Kaoru Hana wa Rin to Saku"},
	})
	store.seedPending(9, "https://weebcentral.com/series/stale")
	connector := &flakyConnector{
		stubConnector: &stubConnector{
			key: "weebcentral",
			searchResults: map[string][]connectors.MangaResult{
				"Kaoru Hana wa Rin to Saku": {{Title: "Kaoru Hana wa Rin to Saku", URL: "https://weebcentral.com/series/01X"}},
			},
		},
		failFor: map[string]error{"The Fragrant Flower Blooms With Dignity": errors.New("HTTP 502")},
	}

	scanner, _ := runScanLogged(t, store, connector)

	if found := store.stored[9]; len(found) != 1 || found[0].CandidateURL != "https://weebcentral.com/series/01X" {
		t.Fatalf("stored = %+v, want the candidate the alternate title found", found)
	}
	progress := scanner.StatusFor(1).Progress
	if progress.WithCandidates != 1 || progress.SearchFailed != 0 || progress.LastError != "" {
		t.Fatalf("progress = %+v, want the tracker counted as found, not as failed", progress)
	}
}

// TestScanKeepsPendingCandidatesWhenAnAlternateSearchFails: the title query is
// answered with nothing and the alternate-title query fails. The alternate is
// exactly the query that finds a series catalogued under another name, so the
// tracker was not fully searched and keeps what it had.
func TestScanKeepsPendingCandidatesWhenAnAlternateSearchFails(t *testing.T) {
	store := newPendingStore(repository.LinkScanTarget{
		TrackerID: 9, Title: "The Fragrant Flower Blooms With Dignity",
		RelatedTitles: []string{"Kaoru Hana wa Rin to Saku"},
	})
	store.seedPending(9, "https://weebcentral.com/series/01X")
	connector := &flakyConnector{
		stubConnector: &stubConnector{key: "weebcentral"},
		failFor:       map[string]error{"Kaoru Hana wa Rin to Saku": errors.New("HTTP 502")},
	}

	scanner, _ := runScanLogged(t, store, connector)

	if len(connector.searchCalls) != 2 {
		t.Fatalf("searched %v, want the title and then its alternate", connector.searchCalls)
	}
	if kept := store.stored[9]; store.replaced[9] != 0 || len(kept) != 1 {
		t.Fatalf("a half-searched tracker lost its candidate: replaced %d times, now %+v", store.replaced[9], kept)
	}
	if got := scanner.StatusFor(1).Progress.SearchFailed; got != 1 {
		t.Fatalf("SearchFailed = %d, want 1", got)
	}
}
