package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type fakeRepo struct {
	items         []repository.PollingTracker
	updatedCount  int
	updatedLatest *float64
	updatedAt     *time.Time
}

func (f *fakeRepo) ListForPolling() ([]repository.PollingTracker, error) {
	return f.items, nil
}

func (f *fakeRepo) UpdatePollingState(_ int64, latestKnownChapter *float64, latestReleaseAt *time.Time, _ time.Time) error {
	f.updatedCount++
	f.updatedLatest = latestKnownChapter
	f.updatedAt = latestReleaseAt
	return nil
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
}

func TestPollerRunOnce_LeavesReleaseDateUnsetWhenChapterNotAdvanced(t *testing.T) {
	prev := 10.0
	next := 10.0
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
	if repo.updatedAt != nil {
		t.Fatalf("expected release date to remain unset when chapter does not advance")
	}
}

func TestPollerRunOnce_UsesCheckedTimeWhenNewChapterHasNoReleaseDate(t *testing.T) {
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

	if repo.updatedAt == nil {
		t.Fatalf("expected fallback release date to be set when chapter increases")
	}
}
