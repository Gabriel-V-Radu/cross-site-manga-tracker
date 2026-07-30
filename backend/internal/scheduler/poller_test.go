package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type fakeRepo struct {
	items             []repository.PollingTracker
	updatedCount      int
	updatedItemID     *string
	updatedURL        string
	updatedLatest     *float64
	updatedAt         *time.Time
	updatedSourceID   int64
	updatedCurrentURL string
	clearedReleaseAt  bool
}

func (f *fakeRepo) ListForPolling() ([]repository.PollingTracker, error) {
	return f.items, nil
}

func (f *fakeRepo) UpdatePollingState(_ int64, sourceID int64, currentSourceURL string, sourceItemID *string, sourceURL string, latestKnownChapter *float64, latestReleaseAt *time.Time, _ bool, _ time.Time) error {
	f.updatedCount++
	f.updatedItemID = sourceItemID
	f.updatedURL = sourceURL
	f.updatedLatest = latestKnownChapter
	f.updatedAt = latestReleaseAt
	f.updatedSourceID = sourceID
	f.updatedCurrentURL = currentSourceURL
	return nil
}

// scriptedConnector is a connector with a chosen key that either fails or
// returns a fixed result, for exercising primary/fallback source selection.
type scriptedConnector struct {
	key    string
	result *connectors.MangaResult
	err    error
}

func (s scriptedConnector) Key() string                       { return s.key }
func (s scriptedConnector) Name() string                      { return s.key }
func (s scriptedConnector) Kind() string                      { return connectors.KindNative }
func (s scriptedConnector) HealthCheck(context.Context) error { return s.err }
func (s scriptedConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (s scriptedConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

type fakeConnector struct {
	latest      *float64
	releaseDate *time.Time
}

func (f fakeConnector) Key() string                       { return "testsource" }
func (f fakeConnector) Name() string                      { return "Test Source" }
func (f fakeConnector) Kind() string                      { return connectors.KindNative }
func (f fakeConnector) HealthCheck(context.Context) error { return nil }
func (f fakeConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (f fakeConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return &connectors.MangaResult{SourceKey: f.Key(), SourceItemID: "a", Title: "T", URL: "u", LatestChapter: f.latest, LastUpdatedAt: f.releaseDate}, nil
}

func TestPollerRunOnce_UpdatesPollingState(t *testing.T) {
	prev := 10.0
	next := 11.0
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &next}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedCount != 1 {
		t.Fatalf("expected 1 update call, got %d", repo.updatedCount)
	}
	if repo.updatedLatest == nil || *repo.updatedLatest != next {
		t.Fatalf("expected latest chapter %.2f, got %#v", next, repo.updatedLatest)
	}
	if repo.updatedURL != "u" {
		t.Fatalf("expected canonical source url to be saved, got %q", repo.updatedURL)
	}
	if repo.updatedItemID == nil || *repo.updatedItemID != "a" {
		t.Fatalf("expected canonical source item id to be saved, got %#v", repo.updatedItemID)
	}
}

func TestPollerRunOnce_LeavesReleaseDateUnsetWhenChapterNotAdvanced(t *testing.T) {
	prev := 10.0
	next := 10.0
	storedReleaseAt := time.Now().UTC().Add(-6 * time.Hour)
	sourceReleaseAt := time.Now().UTC()
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev, LatestReleaseAt: &storedReleaseAt}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &next, releaseDate: &sourceReleaseAt}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedCount != 1 {
		t.Fatalf("expected 1 update call, got %d", repo.updatedCount)
	}
	if repo.updatedAt != nil {
		t.Fatalf("expected stored release date to be kept when chapter does not advance, got %v", repo.updatedAt)
	}
}

func TestPollerRunOnce_FillsMissingReleaseDateWhenChapterNotAdvanced(t *testing.T) {
	prev := 10.0
	next := 10.0
	sourceReleaseAt := time.Now().UTC().Add(-3 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &next, releaseDate: &sourceReleaseAt}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedAt == nil || !repo.updatedAt.Equal(sourceReleaseAt) {
		t.Fatalf("expected source release date to fill missing stored date, got %v", repo.updatedAt)
	}
}

func TestPollerRunOnce_UpdatesReleaseDateWhenChapterAdvances(t *testing.T) {
	prev := 10.0
	next := 11.0
	storedReleaseAt := time.Now().UTC().Add(-72 * time.Hour)
	sourceReleaseAt := time.Now().UTC().Add(-1 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev, LatestReleaseAt: &storedReleaseAt}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &next, releaseDate: &sourceReleaseAt}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedAt == nil || !repo.updatedAt.Equal(sourceReleaseAt) {
		t.Fatalf("expected release date to update when chapter advances, got %v", repo.updatedAt)
	}
}

func TestPollerRunOnce_SkipsRecentlyCheckedIdleTrackers(t *testing.T) {
	latest := 12.0
	recentCheck := time.Now().UTC().Add(-1 * time.Hour)
	staleCheck := time.Now().UTC().Add(-24 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{
		{ID: 1, Title: "Reading recently checked", Status: "reading", SourceURL: "https://example/1", SourceKey: "testsource", LastCheckedAt: &recentCheck},
		{ID: 2, Title: "Completed recently checked", Status: "completed", SourceURL: "https://example/2", SourceKey: "testsource", LastCheckedAt: &recentCheck},
		{ID: 3, Title: "Completed stale check", Status: "completed", SourceURL: "https://example/3", SourceKey: "testsource", LastCheckedAt: &staleCheck},
		{ID: 4, Title: "Dropped never checked", Status: "dropped", SourceURL: "https://example/4", SourceKey: "testsource"},
	}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &latest}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute, IdleInterval: 12 * time.Hour}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	// The reading tracker always polls; the recently checked completed one is
	// skipped; the stale and never-checked idle ones still poll.
	if repo.updatedCount != 3 {
		t.Fatalf("expected 3 update calls, got %d", repo.updatedCount)
	}
}

// TestPollerRunOnce_FallsBackToAlternateSource covers the case this feature
// exists for: the primary site is unreadable (blocked, moved) but the tracker has
// another linked source that still answers.
func TestPollerRunOnce_FallsBackToAlternateSource(t *testing.T) {
	prev := 10.0
	next := 12.0
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceID:           7,
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &prev,
		AlternateSources: []repository.PollingTrackerSource{
			{SourceID: 9, SourceKey: "mirrorsource", SourceURL: "https://mirror/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("behind a browser challenge")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "mirrorsource", result: &connectors.MangaResult{
		SourceKey: "mirrorsource", SourceItemID: "mirror-id", URL: "https://mirror/series", LatestChapter: &next,
	}}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedCount != 1 {
		t.Fatalf("expected the fallback source to produce 1 update, got %d", repo.updatedCount)
	}
	if repo.updatedLatest == nil || *repo.updatedLatest != next {
		t.Fatalf("expected latest chapter %.2f from the fallback, got %#v", next, repo.updatedLatest)
	}

	// The primary pointer must survive untouched: writing the mirror's id/URL
	// into it would leave source_id naming one site and source_url another.
	if repo.updatedItemID != nil {
		t.Fatalf("fallback must not rewrite source_item_id, got %#v", repo.updatedItemID)
	}
	if repo.updatedURL != "" {
		t.Fatalf("fallback must not rewrite source_url, got %q", repo.updatedURL)
	}
	if repo.updatedSourceID != 0 || repo.updatedCurrentURL != "" {
		t.Fatalf("fallback must not touch tracker_sources, got sourceID=%d url=%q", repo.updatedSourceID, repo.updatedCurrentURL)
	}
}

// TestPollerRunOnce_FallbackNeverClearsStoredReleaseDate guards the case that
// broke in production: a fallback source with no release date of its own must not
// wipe the date the primary source recorded, even when the chapter advances.
func TestPollerRunOnce_FallbackNeverClearsStoredReleaseDate(t *testing.T) {
	prev := 100.0
	advanced := 112.0
	storedReleaseAt := time.Now().UTC().Add(-48 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &prev,
		LatestReleaseAt:    &storedReleaseAt,
		AlternateSources: []repository.PollingTrackerSource{
			{SourceKey: "mirrorsource", SourceURL: "https://mirror/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	// The mirror advances the chapter but publishes no release date at all.
	if err := registry.Register(scriptedConnector{key: "mirrorsource", result: &connectors.MangaResult{
		SourceKey: "mirrorsource", LatestChapter: &advanced, LastUpdatedAt: nil,
	}}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != advanced {
		t.Fatalf("expected the chapter to advance to %.1f, got %#v", advanced, repo.updatedLatest)
	}
	if repo.clearedReleaseAt {
		t.Fatalf("fallback must not clear the stored release date")
	}
	if repo.updatedAt != nil {
		t.Fatalf("fallback has no date of its own to write, got %v", repo.updatedAt)
	}
}

// TestPollerRunOnce_FallbackNeverLowersLatestChapter guards against a lagging
// mirror resurrecting chapters the user already read.
func TestPollerRunOnce_FallbackNeverLowersLatestChapter(t *testing.T) {
	prev := 100.0
	behind := 84.0
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &prev,
		AlternateSources: []repository.PollingTrackerSource{
			{SourceKey: "mirrorsource", SourceURL: "https://mirror/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "mirrorsource", result: &connectors.MangaResult{
		SourceKey: "mirrorsource", LatestChapter: &behind,
	}}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != prev {
		t.Fatalf("expected latest chapter to stay at %.2f, got %#v", prev, repo.updatedLatest)
	}
}

// TestPollerRunOnce_NoUpdateWhenPrimaryFailsWithoutAlternates preserves the
// pre-existing behaviour: a failing source with nothing to fall back to is
// skipped, never written with partial data.
func TestPollerRunOnce_NoUpdateWhenPrimaryFailsWithoutAlternates(t *testing.T) {
	prev := 10.0
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &prev,
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedCount != 0 {
		t.Fatalf("expected no update when the only source fails, got %d", repo.updatedCount)
	}
}

func TestPollerRunOnce_LeavesReleaseDateUnsetWhenNewChapterHasNoReleaseDate(t *testing.T) {
	prev := 340.0
	next := 341.0
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &next, releaseDate: nil}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedAt != nil {
		t.Fatalf("expected release date to remain unset when source does not provide one")
	}
}
