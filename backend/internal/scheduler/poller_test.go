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
	items               []repository.PollingTracker
	updatedCount        int
	updatedItemID       *string
	updatedURL          string
	updatedLatest       *float64
	updatedAt           *time.Time
	updatedSourceID     int64
	updatedCurrentURL   string
	clearedReleaseAt    bool
	updatedPendingLower *float64
}

func (f *fakeRepo) ListForPolling() ([]repository.PollingTracker, error) {
	return f.items, nil
}

func (f *fakeRepo) UpdatePollingState(update repository.PollingUpdate) error {
	f.updatedCount++
	f.updatedItemID = update.SourceItemID
	f.updatedURL = update.SourceURL
	f.updatedLatest = update.LatestKnownChapter
	f.updatedAt = update.LatestReleaseAt
	f.updatedSourceID = update.SourceID
	f.updatedCurrentURL = update.CurrentSourceURL
	f.clearedReleaseAt = update.ClearLatestReleaseAt
	f.updatedPendingLower = update.PendingLowerChapter
	return nil
}

func chapterPtr(value float64) *float64 { return &value }

// TestDecideChapter covers reconciling a reported chapter against the stored one.
// The two failure modes pull in opposite directions: a mirror that lags must not
// walk a tracker backwards, while a stored number that is simply wrong must not
// stay frozen forever. MangaFire produced the second case by reporting the
// highest chapter in any language, leaving trackers holding a raw-Japanese number
// no English mirror could ever correct.
func TestDecideChapter(t *testing.T) {
	const window = 24 * time.Hour
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-48 * time.Hour)
	recently := now.Add(-1 * time.Hour)

	cases := []struct {
		name             string
		stored           *float64
		lastRead         *float64
		pending          *float64
		pendingSeenAt    *time.Time
		reported         *float64
		usedFallback     bool
		wantLatest       *float64
		wantPendingLower *float64
		wantCorrected    bool
	}{
		{
			name:       "nothing reported keeps the stored number",
			stored:     chapterPtr(302),
			reported:   nil,
			wantLatest: chapterPtr(302),
		},
		{
			name:       "no stored number takes whatever is reported",
			reported:   chapterPtr(176),
			wantLatest: chapterPtr(176),
		},
		{
			name:         "a fallback advance is applied",
			stored:       chapterPtr(53),
			reported:     chapterPtr(57),
			usedFallback: true,
			wantLatest:   chapterPtr(57),
		},
		{
			name:             "an advance clears a pending correction",
			stored:           chapterPtr(53),
			pending:          chapterPtr(50),
			pendingSeenAt:    &longAgo,
			reported:         chapterPtr(57),
			usedFallback:     true,
			wantLatest:       chapterPtr(57),
			wantPendingLower: nil,
		},
		{
			name:             "agreement clears a pending correction",
			stored:           chapterPtr(176),
			pending:          chapterPtr(170),
			pendingSeenAt:    &longAgo,
			reported:         chapterPtr(176),
			usedFallback:     true,
			wantLatest:       chapterPtr(176),
			wantPendingLower: nil,
		},
		{
			name:             "a first lower report is remembered, not applied",
			stored:           chapterPtr(302),
			reported:         chapterPtr(176),
			usedFallback:     true,
			wantLatest:       chapterPtr(302),
			wantPendingLower: chapterPtr(176),
		},
		{
			name:             "a lower report still inside the window is not applied",
			stored:           chapterPtr(302),
			pending:          chapterPtr(176),
			pendingSeenAt:    &recently,
			reported:         chapterPtr(176),
			usedFallback:     true,
			wantLatest:       chapterPtr(302),
			wantPendingLower: chapterPtr(176),
		},
		{
			name:          "a lower report confirmed past the window is applied",
			stored:        chapterPtr(302),
			pending:       chapterPtr(176),
			pendingSeenAt: &longAgo,
			reported:      chapterPtr(176),
			usedFallback:  true,
			wantLatest:    chapterPtr(176),
			wantCorrected: true,
		},
		{
			name:             "a different lower report restarts confirmation",
			stored:           chapterPtr(302),
			pending:          chapterPtr(176),
			pendingSeenAt:    &longAgo,
			reported:         chapterPtr(170),
			usedFallback:     true,
			wantLatest:       chapterPtr(302),
			wantPendingLower: chapterPtr(170),
		},
		{
			name:          "a primary source corrects downwards immediately",
			stored:        chapterPtr(302),
			reported:      chapterPtr(176),
			usedFallback:  false,
			wantLatest:    chapterPtr(176),
			wantCorrected: true,
		},
		{
			name:          "a correction is clamped at the read position",
			stored:        chapterPtr(302),
			lastRead:      chapterPtr(200),
			pending:       chapterPtr(176),
			pendingSeenAt: &longAgo,
			reported:      chapterPtr(176),
			usedFallback:  true,
			wantLatest:    chapterPtr(200),
			wantCorrected: true,
		},
		{
			name:       "a correction entirely below the read position is refused",
			stored:     chapterPtr(302),
			lastRead:   chapterPtr(302),
			reported:   chapterPtr(176),
			wantLatest: chapterPtr(302),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tracker := repository.PollingTracker{
				LatestKnownChapter:      testCase.stored,
				LastReadChapter:         testCase.lastRead,
				PendingLowerChapter:     testCase.pending,
				PendingLowerFirstSeenAt: testCase.pendingSeenAt,
			}

			got := decideChapter(tracker, testCase.reported, testCase.usedFallback, now, window)

			assertChapter(t, "latest", got.latest, testCase.wantLatest)
			assertChapter(t, "pendingLower", got.pendingLower, testCase.wantPendingLower)
			if got.corrected != testCase.wantCorrected {
				t.Fatalf("expected corrected=%v, got %v", testCase.wantCorrected, got.corrected)
			}
		})
	}
}

func assertChapter(t *testing.T, label string, got *float64, want *float64) {
	t.Helper()
	switch {
	case want == nil && got == nil:
		return
	case want == nil:
		t.Fatalf("expected %s to be unset, got %.1f", label, *got)
	case got == nil:
		t.Fatalf("expected %s to be %.1f, got unset", label, *want)
	case *got != *want:
		t.Fatalf("expected %s to be %.1f, got %.1f", label, *want, *got)
	}
}

// TestPollerRunOnce_RecordsPendingLowerChapter proves the decision reaches the
// repository: a lagging mirror leaves the stored number alone but its report is
// persisted, which is what lets a later poll confirm it.
func TestPollerRunOnce_RecordsPendingLowerChapter(t *testing.T) {
	stored := 302.0
	reported := 176.0
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &stored,
		AlternateSources: []repository.TrackerSourceRef{
			{SourceKey: "mirrorsource", SourceURL: "https://mirror/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "mirrorsource", result: &connectors.MangaResult{
		SourceKey: "mirrorsource", LatestChapter: &reported,
	}}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != stored {
		t.Fatalf("expected the stored chapter to survive the first lower report, got %#v", repo.updatedLatest)
	}
	if repo.updatedPendingLower == nil || *repo.updatedPendingLower != reported {
		t.Fatalf("expected the lower report to be recorded as pending, got %#v", repo.updatedPendingLower)
	}
}

// TestPollerRunOnce_AppliesConfirmedLowerChapter is the same tracker one window
// later: the mirror has kept saying the same thing, so the wrong stored number is
// finally replaced instead of staying frozen.
func TestPollerRunOnce_AppliesConfirmedLowerChapter(t *testing.T) {
	stored := 302.0
	reported := 176.0
	pending := 176.0
	firstSeen := time.Now().UTC().Add(-48 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                      1,
		Title:                   "A",
		Status:                  "reading",
		SourceURL:               "https://blocked/series",
		SourceKey:               "blockedsource",
		LatestKnownChapter:      &stored,
		PendingLowerChapter:     &pending,
		PendingLowerFirstSeenAt: &firstSeen,
		AlternateSources: []repository.TrackerSourceRef{
			{SourceKey: "mirrorsource", SourceURL: "https://mirror/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "mirrorsource", result: &connectors.MangaResult{
		SourceKey: "mirrorsource", LatestChapter: &reported,
	}}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute, LowerConfirmationDelay: 24 * time.Hour}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != reported {
		t.Fatalf("expected the confirmed correction to be applied, got %#v", repo.updatedLatest)
	}
	if repo.updatedPendingLower != nil {
		t.Fatalf("expected the pending record to be cleared once applied, got %#v", repo.updatedPendingLower)
	}
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

// A source that had been reporting too high a number — MangaFire counting
// Japanese chapters the reader does not follow — corrects itself downward. The
// stored date belongs to the chapter that number is being taken away from, so it
// has to be replaced along with it, not preserved as if nothing happened.
func TestPollerRunOnce_UpdatesReleaseDateWhenChapterIsCorrectedDownward(t *testing.T) {
	prev := 57.5
	corrected := 54.0
	storedReleaseAt := time.Now().UTC().Add(-180 * 24 * time.Hour)
	sourceReleaseAt := time.Now().UTC().Add(-2 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev, LatestReleaseAt: &storedReleaseAt}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &corrected, releaseDate: &sourceReleaseAt}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != corrected {
		t.Fatalf("expected the corrected chapter %.1f to be stored, got %#v", corrected, repo.updatedLatest)
	}
	if repo.updatedAt == nil || !repo.updatedAt.Equal(sourceReleaseAt) {
		t.Fatalf("expected the corrected chapter's release date to replace the stale one, got %v", repo.updatedAt)
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
		AlternateSources: []repository.TrackerSourceRef{
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
		AlternateSources: []repository.TrackerSourceRef{
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
		AlternateSources: []repository.TrackerSourceRef{
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

// TestPollerRunOnce_FallbackPrefersFreshestAlternate pins why the fallback
// consults every alternate instead of stopping at the first that answers: a
// mirror that is alive but stale (MangaBuddy lags releases by chapters) must
// not shadow a fresher tracking source linked after it (MangaUpdates).
func TestPollerRunOnce_FallbackPrefersFreshestAlternate(t *testing.T) {
	prev := 100.0
	stale := 102.0
	fresh := 105.0
	freshDate := time.Now().UTC().Add(-2 * time.Hour)
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &prev,
		AlternateSources: []repository.TrackerSourceRef{
			{SourceKey: "stalemirror", SourceURL: "https://stale/series"},
			{SourceKey: "freshtracker", SourceURL: "https://fresh/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "stalemirror", result: &connectors.MangaResult{
		SourceKey: "stalemirror", LatestChapter: &stale,
	}}); err != nil {
		t.Fatalf("register stale connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "freshtracker", result: &connectors.MangaResult{
		SourceKey: "freshtracker", LatestChapter: &fresh, LastUpdatedAt: &freshDate,
	}}); err != nil {
		t.Fatalf("register fresh connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != fresh {
		t.Fatalf("expected the freshest alternate's chapter %.1f, got %#v", fresh, repo.updatedLatest)
	}
	if repo.updatedAt == nil || !repo.updatedAt.Equal(freshDate) {
		t.Fatalf("expected the freshest alternate's release date, got %v", repo.updatedAt)
	}
}

// TestImplausibleChapterAdvance pins the margin that separates a real advance
// from the junk numbers sources have reported in production: years parsed as
// chapters, numbers lifted from series titles, placeholder entries.
func TestImplausibleChapterAdvance(t *testing.T) {
	cases := []struct {
		name     string
		stored   *float64
		reported float64
		want     bool
	}{
		{name: "no stored number accepts anything", stored: nil, reported: 10000, want: false},
		{name: "young trackers may catch up freely", stored: chapterPtr(5), reported: 600, want: false},
		{name: "cross-language inflation stays inside the margin", stored: chapterPtr(176), reported: 302, want: false},
		{name: "just under the margin is accepted", stored: chapterPtr(230), reported: 560, want: false},
		{name: "a placeholder chapter 1000 over a run of 230 is rejected", stored: chapterPtr(230), reported: 1000, want: true},
		{name: "a year reported as a chapter is rejected", stored: chapterPtr(271), reported: 2019, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := implausibleChapterAdvance(tc.stored, tc.reported); got != tc.want {
				t.Fatalf("implausibleChapterAdvance(%v, %v) = %v, want %v", tc.stored, tc.reported, got, tc.want)
			}
		})
	}
}

// TestPollerRunOnce_IgnoresImplausibleJumpFromPrimary guards the stored chapter
// against a primary source reporting a junk number: once stored, the same
// source re-confirms it every cycle and nothing can ever walk it back.
func TestPollerRunOnce_IgnoresImplausibleJumpFromPrimary(t *testing.T) {
	prev := 271.0
	junk := 2019.0
	repo := &fakeRepo{items: []repository.PollingTracker{{ID: 1, Title: "A", Status: "reading", SourceURL: "https://example", SourceKey: "testsource", LatestKnownChapter: &prev}}}
	registry := connectors.NewRegistry()
	if err := registry.Register(fakeConnector{latest: &junk}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != prev {
		t.Fatalf("expected the junk jump to be ignored and %.0f kept, got %#v", prev, repo.updatedLatest)
	}
	if repo.clearedReleaseAt {
		t.Fatalf("an ignored number must not clear the stored release date")
	}
}

// TestPollerRunOnce_FallbackIgnoresImplausibleJumpAndUsesSaneMirror pins why
// junk answers are filtered before ranking the alternates: the highest number
// wins that comparison, so one source's junk would shadow a real advance from
// another mirror.
func TestPollerRunOnce_FallbackIgnoresImplausibleJumpAndUsesSaneMirror(t *testing.T) {
	prev := 230.0
	junk := 1000.0
	sane := 232.0
	repo := &fakeRepo{items: []repository.PollingTracker{{
		ID:                 1,
		Title:              "A",
		Status:             "reading",
		SourceURL:          "https://blocked/series",
		SourceKey:          "blockedsource",
		LatestKnownChapter: &prev,
		AlternateSources: []repository.TrackerSourceRef{
			{SourceKey: "junkmirror", SourceURL: "https://junk/series"},
			{SourceKey: "sanemirror", SourceURL: "https://sane/series"},
		},
	}}}

	registry := connectors.NewRegistry()
	if err := registry.Register(scriptedConnector{key: "blockedsource", err: errors.New("blocked")}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "junkmirror", result: &connectors.MangaResult{
		SourceKey: "junkmirror", LatestChapter: &junk,
	}}); err != nil {
		t.Fatalf("register junk connector: %v", err)
	}
	if err := registry.Register(scriptedConnector{key: "sanemirror", result: &connectors.MangaResult{
		SourceKey: "sanemirror", LatestChapter: &sane,
	}}); err != nil {
		t.Fatalf("register sane connector: %v", err)
	}

	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	if repo.updatedLatest == nil || *repo.updatedLatest != sane {
		t.Fatalf("expected the sane mirror's %.0f to win over junk, got %#v", sane, repo.updatedLatest)
	}
}

// TestBetterFallbackResult pins the ranking between fallback answers.
func TestBetterFallbackResult(t *testing.T) {
	date := time.Now().UTC()
	withChapter := func(chapter float64) *connectors.MangaResult {
		return &connectors.MangaResult{LatestChapter: &chapter}
	}
	withChapterAndDate := func(chapter float64) *connectors.MangaResult {
		return &connectors.MangaResult{LatestChapter: &chapter, LastUpdatedAt: &date}
	}

	if !betterFallbackResult(nil, &connectors.MangaResult{}) {
		t.Fatal("any answer beats none")
	}
	if betterFallbackResult(withChapter(10), &connectors.MangaResult{}) {
		t.Fatal("a chapterless answer must not replace a chaptered one")
	}
	if !betterFallbackResult(&connectors.MangaResult{}, withChapter(10)) {
		t.Fatal("a chaptered answer must replace a chapterless one")
	}
	if !betterFallbackResult(withChapter(10), withChapter(12)) {
		t.Fatal("a higher chapter must win")
	}
	if betterFallbackResult(withChapter(12), withChapter(10)) {
		t.Fatal("a lower chapter must not win")
	}
	if !betterFallbackResult(withChapter(10), withChapterAndDate(10)) {
		t.Fatal("on equal chapters, a release date must win")
	}
	if betterFallbackResult(withChapterAndDate(10), withChapter(10)) {
		t.Fatal("on equal chapters, no date must not replace a date")
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
