package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// siteDouble is a linked site whose answer a test can change between cycles,
// and which counts how often it was asked.
type siteDouble struct {
	key      string
	trailing bool

	mu      sync.Mutex
	chapter *float64
	err     error

	calls atomic.Int32
}

func newSite(key string, chapter float64) *siteDouble {
	return &siteDouble{key: key, chapter: chapterPtr(chapter)}
}

func (s *siteDouble) Key() string                       { return s.key }
func (s *siteDouble) Name() string                      { return s.key }
func (s *siteDouble) Kind() string                      { return connectors.KindNative }
func (s *siteDouble) HealthCheck(context.Context) error { return nil }
func (s *siteDouble) TrailingChapterFeed() bool         { return s.trailing }
func (s *siteDouble) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (s *siteDouble) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var chapter *float64
	if s.chapter != nil {
		value := *s.chapter
		chapter = &value
	}
	return &connectors.MangaResult{SourceKey: s.key, LatestChapter: chapter}, nil
}

func (s *siteDouble) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func registryOf(t *testing.T, sites ...connectors.Connector) *connectors.Registry {
	t.Helper()
	registry := connectors.NewRegistry()
	for _, site := range sites {
		if err := registry.Register(site); err != nil {
			t.Fatalf("register %s: %v", site.Key(), err)
		}
	}
	return registry
}

// statefulRepo applies every write back to its trackers, the way the real
// repository does, so a test can run cycle after cycle and watch the rotation
// move.
type statefulRepo struct {
	mu    sync.Mutex
	items []*repository.PollingTracker
}

func (r *statefulRepo) ListForPolling(context.Context) ([]repository.PollingTracker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]repository.PollingTracker, 0, len(r.items))
	for _, item := range r.items {
		copied := *item
		copied.AlternateSources = append([]repository.TrackerSourceRef(nil), item.AlternateSources...)
		out = append(out, copied)
	}
	return out, nil
}

func (r *statefulRepo) find(id int64) *repository.PollingTracker {
	for _, item := range r.items {
		if item.ID == id {
			return item
		}
	}
	return nil
}

func (r *statefulRepo) UpdatePollingState(_ context.Context, update repository.PollingUpdate) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tracker := r.find(update.TrackerID)
	tracker.LatestKnownChapter = update.LatestKnownChapter
	if update.LatestChapterSourceID != nil {
		id := *update.LatestChapterSourceID
		tracker.LatestChapterSourceID = &id
	}
	switch {
	case update.PendingLowerChapter == nil:
		tracker.PendingLowerFirstSeenAt = nil
	case tracker.PendingLowerChapter == nil || *tracker.PendingLowerChapter != *update.PendingLowerChapter:
		seenAt := update.CheckedAt
		tracker.PendingLowerFirstSeenAt = &seenAt
	}
	tracker.PendingLowerChapter = update.PendingLowerChapter
	checkedAt := update.CheckedAt
	tracker.LastCheckedAt = &checkedAt
	applySourcePolls(tracker, update.CheckedAt, update.SourcePolls)
	return true, nil
}

func (r *statefulRepo) MarkPollCheckedAt(_ context.Context, trackerID int64, checkedAt time.Time, polls []repository.SourcePoll) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tracker := r.find(trackerID)
	tracker.LastCheckedAt = &checkedAt
	applySourcePolls(tracker, checkedAt, polls)
	return nil
}

func applySourcePolls(tracker *repository.PollingTracker, at time.Time, polls []repository.SourcePoll) {
	for _, poll := range polls {
		polledAt := at
		if poll.Primary {
			tracker.PrimaryLastPolledAt = &polledAt
			continue
		}
		for index := range tracker.AlternateSources {
			alternate := &tracker.AlternateSources[index]
			if alternate.SourceID != poll.SourceID || alternate.SourceURL != poll.SourceURL {
				continue
			}
			alternate.LastPolledAt = &polledAt
			if poll.Answered {
				alternate.InStep = poll.InStep
			}
		}
	}
}

// rotatingTracker links a primary ("alpha", source 1) and the given
// alternates, every link in step and none polled yet.
func rotatingTracker(id int64, stored float64, alternates ...string) *repository.PollingTracker {
	tracker := &repository.PollingTracker{
		ID: id, Title: "T", Status: "reading",
		SourceID: 1, SourceKey: "alpha", SourceURL: "https://alpha/series",
		LatestKnownChapter: chapterPtr(stored),
	}
	for index, key := range alternates {
		tracker.AlternateSources = append(tracker.AlternateSources, repository.TrackerSourceRef{
			SourceID: int64(index + 2), SourceKey: key, SourceURL: "https://" + key + "/series", InStep: true,
		})
	}
	return tracker
}

// cyclingPoller returns a poller whose clock reads the time step() last set.
func cyclingPoller(repo pollRepository, registry *connectors.Registry) (*Poller, func(int)) {
	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	var current atomic.Int64
	current.Store(base.UnixNano())
	poller.now = func() time.Time { return time.Unix(0, current.Load()).UTC() }
	return poller, func(cycle int) { current.Store(base.Add(time.Duration(cycle) * time.Hour).UnixNano()) }
}

// The first cycle after the rotation goes live finds no link polled yet. It
// must still spread the library evenly across the linked sites rather than
// start every tracker on its primary — last-polled order keeps whatever phase
// the trackers start in, so a lopsided first cycle would stay lopsided.
func TestRotationSpreadsTheFirstCycleAcrossLinkedSites(t *testing.T) {
	repo := &statefulRepo{}
	for id := int64(1); id <= 6; id++ {
		repo.items = append(repo.items, rotatingTracker(id, 10, "beta", "gamma"))
	}
	alpha, beta, gamma := newSite("alpha", 10), newSite("beta", 10), newSite("gamma", 10)
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta, gamma))

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}

	for _, site := range []*siteDouble{alpha, beta, gamma} {
		if got := site.calls.Load(); got != 2 {
			t.Fatalf("expected each site asked for 2 of the 6 trackers, %s was asked %d times", site.key, got)
		}
	}
}

// The bound the rotation exists to keep: a chapter that only one of the three
// sites carries yet is found within three cycles, one request per cycle, and
// the sites still lagging afterwards cannot walk the tracker back.
func TestRotationFindsAChapterOnlyOneSiteCarriesWithinThreeCycles(t *testing.T) {
	tracker := rotatingTracker(1, 10, "beta", "gamma")
	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	// gamma, the only site with the new chapter, is the last one due.
	alphaAt, betaAt, gammaAt := base.Add(-3*time.Hour), base.Add(-2*time.Hour), base.Add(-1*time.Hour)
	tracker.PrimaryLastPolledAt = &alphaAt
	tracker.AlternateSources[0].LastPolledAt = &betaAt
	tracker.AlternateSources[1].LastPolledAt = &gammaAt
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}

	alpha, beta, gamma := newSite("alpha", 10), newSite("beta", 10), newSite("gamma", 11)
	poller, step := cyclingPoller(repo, registryOf(t, alpha, beta, gamma))

	for cycle := 0; cycle < 3; cycle++ {
		step(cycle)
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if asked := alpha.calls.Load() + beta.calls.Load() + gamma.calls.Load(); asked != int32(cycle+1) {
			t.Fatalf("cycle %d: expected one request per cycle, %d so far", cycle, asked)
		}
		stored := *repo.items[0].LatestKnownChapter
		if cycle < 2 && stored != 10 {
			t.Fatalf("cycle %d: gamma has not been asked yet, but the tracker reads %.0f", cycle, stored)
		}
		if cycle == 2 && stored != 11 {
			t.Fatalf("the third cycle asks gamma and must find chapter 11, tracker reads %.0f", stored)
		}
	}
	if alpha.calls.Load() != 1 || beta.calls.Load() != 1 || gamma.calls.Load() != 1 {
		t.Fatalf("expected each site asked once over three cycles, got alpha=%d beta=%d gamma=%d",
			alpha.calls.Load(), beta.calls.Load(), gamma.calls.Load())
	}

	// alpha's turn comes back while it still lags: its lower number waits for
	// confirmation instead of undoing gamma's find.
	step(3)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if alpha.calls.Load() != 2 {
		t.Fatalf("expected the fourth cycle to come back to alpha, got alpha=%d", alpha.calls.Load())
	}
	if stored := *repo.items[0].LatestKnownChapter; stored != 11 {
		t.Fatalf("a lagging primary walked the tracker back to %.0f", stored)
	}
	if pending := repo.items[0].PendingLowerChapter; pending == nil || *pending != 10 {
		t.Fatalf("expected alpha's lower number pending, got %#v", pending)
	}
}

// A site that is not in step may take its turn, but its claim of a new chapter
// is not stored on its word alone: the poll asks the next site first.
func TestRotationHoldsAnAdvanceFromASiteNotInStep(t *testing.T) {
	recently := time.Now().UTC().Add(-time.Minute)
	untrustedTurn := func() repository.PollingTracker {
		return repository.PollingTracker{
			ID: 1, Title: "T", Status: "reading",
			SourceID: 1, SourceKey: "alpha", SourceURL: "https://alpha/series",
			LatestKnownChapter:  chapterPtr(10),
			PrimaryLastPolledAt: &recently,
			AlternateSources: []repository.TrackerSourceRef{
				{SourceID: 2, SourceKey: "beta", SourceURL: "https://beta/series"},
			},
		}
	}
	pollFor := func(t *testing.T, tracker repository.PollingTracker, alpha, beta *siteDouble) *fakeRepo {
		t.Helper()
		repo := &fakeRepo{items: []repository.PollingTracker{tracker}}
		poller := NewPoller(repo, registryOf(t, alpha, beta), PollerConfig{Interval: time.Minute}, nil)
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run once: %v", err)
		}
		return repo
	}
	betaPoll := func(t *testing.T, repo *fakeRepo) repository.SourcePoll {
		t.Helper()
		for _, poll := range repo.sourcePolls {
			if poll.SourceID == 2 {
				return poll
			}
		}
		t.Fatalf("beta's poll was not recorded: %+v", repo.sourcePolls)
		return repository.SourcePoll{}
	}

	t.Run("a primary that disagrees keeps the stored chapter", func(t *testing.T) {
		alpha, beta := newSite("alpha", 10), newSite("beta", 11)
		repo := pollFor(t, untrustedTurn(), alpha, beta)
		if beta.calls.Load() != 1 || alpha.calls.Load() != 1 {
			t.Fatalf("expected beta's turn and then alpha, got beta=%d alpha=%d", beta.calls.Load(), alpha.calls.Load())
		}
		assertChapter(t, "latest", repo.updatedLatest, chapterPtr(10))
		if poll := betaPoll(t, repo); !poll.Answered || poll.InStep {
			t.Fatalf("beta answered out of step, got %+v", poll)
		}
	})

	t.Run("a primary that agrees confirms it and brings the site in step", func(t *testing.T) {
		alpha, beta := newSite("alpha", 11), newSite("beta", 11)
		repo := pollFor(t, untrustedTurn(), alpha, beta)
		assertChapter(t, "latest", repo.updatedLatest, chapterPtr(11))
		if repo.updatedLatestSource == nil || *repo.updatedLatestSource != 1 {
			t.Fatalf("the confirming primary is the reporter, got %#v", repo.updatedLatestSource)
		}
		if poll := betaPoll(t, repo); !poll.InStep {
			t.Fatalf("beta's number was confirmed, so it is in step now: %+v", poll)
		}
	})

	t.Run("with nobody else answering the held advance stands", func(t *testing.T) {
		alpha, beta := newSite("alpha", 10), newSite("beta", 11)
		alpha.fail(errors.New("blocked"))
		repo := pollFor(t, untrustedTurn(), alpha, beta)
		assertChapter(t, "latest", repo.updatedLatest, chapterPtr(11))
		if repo.updatedLatestSource == nil || *repo.updatedLatestSource != 2 {
			t.Fatalf("beta is the reporter, got %#v", repo.updatedLatestSource)
		}
	})

	t.Run("a site in step reports alone on its turn", func(t *testing.T) {
		tracker := untrustedTurn()
		tracker.AlternateSources[0].InStep = true
		alpha, beta := newSite("alpha", 10), newSite("beta", 11)
		repo := pollFor(t, tracker, alpha, beta)
		if alpha.calls.Load() != 0 {
			t.Fatalf("the primary must not be asked on beta's turn, got %d", alpha.calls.Load())
		}
		assertChapter(t, "latest", repo.updatedLatest, chapterPtr(11))
		// The primary was not asked, so its identifiers are left alone.
		if repo.updatedSourceID != 0 || repo.updatedURL != "" || repo.updatedItemID != nil {
			t.Fatalf("an alternate's turn must not touch the primary's identifiers, got sourceID=%d url=%q item=%#v",
				repo.updatedSourceID, repo.updatedURL, repo.updatedItemID)
		}
	})
}

// Every linked site confirms the stored number in turn. The site that found it
// keeps the attribution; a confirmation only fills one that is missing.
func TestRotationKeepsTheAttributionWithTheSiteThatFoundTheChapter(t *testing.T) {
	recently := time.Now().UTC().Add(-time.Minute)
	confirmedByBeta := func(storedBy *int64) *fakeRepo {
		tracker := repository.PollingTracker{
			ID: 1, Title: "T", Status: "reading",
			SourceID: 1, SourceKey: "alpha", SourceURL: "https://alpha/series",
			LatestKnownChapter:    chapterPtr(10),
			LatestChapterSourceID: storedBy,
			PrimaryLastPolledAt:   &recently,
			AlternateSources: []repository.TrackerSourceRef{
				{SourceID: 2, SourceKey: "beta", SourceURL: "https://beta/series", InStep: true},
			},
		}
		repo := &fakeRepo{items: []repository.PollingTracker{tracker}}
		poller := NewPoller(repo, registryOf(t, newSite("alpha", 10), newSite("beta", 10)), PollerConfig{Interval: time.Minute}, nil)
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run once: %v", err)
		}
		return repo
	}

	primary := int64(1)
	if repo := confirmedByBeta(&primary); repo.updatedLatestSource != nil {
		t.Fatalf("beta's confirmation must not take over alpha's attribution, got %#v", repo.updatedLatestSource)
	}
	if repo := confirmedByBeta(nil); repo.updatedLatestSource == nil || *repo.updatedLatestSource != 2 {
		t.Fatalf("a confirmation fills a missing attribution, got %#v", repo.updatedLatestSource)
	}
}

// MangaUpdates indexes releases after the hosts publish them, so it never
// takes a turn of its own — it would only ever report a chapter late — but
// it still answers when every other site is dark.
func TestRotationNeverGivesATrailingFeedATurn(t *testing.T) {
	recently := time.Now().UTC().Add(-time.Minute)
	tracker := repository.PollingTracker{
		ID: 1, Title: "T", Status: "reading",
		SourceID: 1, SourceKey: "alpha", SourceURL: "https://alpha/series",
		LatestKnownChapter:  chapterPtr(10),
		PrimaryLastPolledAt: &recently,
		AlternateSources: []repository.TrackerSourceRef{
			{SourceID: 2, SourceKey: "tracking", SourceURL: "https://tracking/series", InStep: true},
		},
	}
	alpha, tracking := newSite("alpha", 10), newSite("tracking", 10)
	tracking.trailing = true

	repo := &fakeRepo{items: []repository.PollingTracker{tracker}}
	poller := NewPoller(repo, registryOf(t, alpha, tracking), PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if tracking.calls.Load() != 0 || alpha.calls.Load() != 1 {
		t.Fatalf("the trailing feed was never polled and is still not due before alpha: tracking=%d alpha=%d",
			tracking.calls.Load(), alpha.calls.Load())
	}

	alpha.fail(errors.New("blocked"))
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if tracking.calls.Load() != 1 {
		t.Fatalf("with alpha dark the trailing feed must still answer as a fallback, got %d", tracking.calls.Load())
	}
}

// At most three sites take turns, which is what keeps the bound at three
// cycles. Sites in step win the seats; the rest answer only as fallbacks.
func TestRotationCapsTheRotationAtThreeSites(t *testing.T) {
	tracker := rotatingTracker(1, 10, "beta", "gamma", "delta")
	tracker.AlternateSources[0].InStep = false
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
	alpha, beta, gamma, delta := newSite("alpha", 10), newSite("beta", 10), newSite("gamma", 10), newSite("delta", 10)
	poller, step := cyclingPoller(repo, registryOf(t, alpha, beta, gamma, delta))

	for cycle := 0; cycle < 6; cycle++ {
		step(cycle)
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	if beta.calls.Load() != 0 {
		t.Fatalf("beta is past the cap and must not take turns, asked %d times", beta.calls.Load())
	}
	for _, site := range []*siteDouble{alpha, gamma, delta} {
		if got := site.calls.Load(); got != 2 {
			t.Fatalf("expected %s asked twice in six cycles, got %d", site.key, got)
		}
	}
}

// A site cooling down is passed without a request and keeps its turn for when
// it recovers; a site that failed is stamped, so the next cycle moves on.
func TestRotationRecordsOnlyTheSitesItReached(t *testing.T) {
	tracker := rotatingTracker(1, 10, "beta", "gamma")
	repo := &fakeRepo{items: []repository.PollingTracker{*tracker}}
	cooling := &coolingConnector{key: "alpha"}
	beta, gamma := newSite("beta", 10), newSite("gamma", 10)
	beta.fail(&connectors.HTTPStatusError{StatusCode: 503})

	poller := NewPoller(repo, registryOf(t, cooling, beta, gamma), PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}

	byID := map[int64]repository.SourcePoll{}
	for _, poll := range repo.sourcePolls {
		byID[poll.SourceID] = poll
	}
	for _, poll := range repo.sourcePolls {
		if poll.Primary {
			t.Fatalf("the cooling-down primary was never reached and must not be stamped: %+v", repo.sourcePolls)
		}
	}
	if poll, ok := byID[2]; !ok || poll.Answered {
		t.Fatalf("beta failed: stamped, but not as an answer, got %+v", poll)
	}
	if poll, ok := byID[3]; !ok || !poll.Answered || !poll.InStep {
		t.Fatalf("gamma answered the stored number, got %+v", poll)
	}
}

// Polls are sharded by the site their turn goes to, not by the primary: two
// trackers created on the same site but due on different ones run in
// parallel. Sharded by primary they would queue behind each other, and the
// first resolve would time out waiting for the second.
func TestRotationShardsByTheSiteWhoseTurnItIs(t *testing.T) {
	recently := time.Now().UTC().Add(-time.Minute)
	repo := &fakeRepo{items: []repository.PollingTracker{
		{ID: 1, Title: "A", Status: "reading", SourceID: 1, SourceKey: "sourceone", SourceURL: "https://one/a"},
		{
			ID: 2, Title: "B", Status: "reading", SourceID: 1, SourceKey: "sourceone", SourceURL: "https://one/b",
			PrimaryLastPolledAt: &recently,
			AlternateSources: []repository.TrackerSourceRef{
				{SourceID: 2, SourceKey: "sourcetwo", SourceURL: "https://two/b", InStep: true},
			},
		},
	}}

	var ready sync.WaitGroup
	ready.Add(2)
	registry := registryOf(t,
		rendezvousConnector{key: "sourceone", ready: &ready},
		rendezvousConnector{key: "sourcetwo", ready: &ready},
	)
	poller := NewPoller(repo, registry, PollerConfig{Interval: time.Minute}, nil)
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if repo.updatedCount != 2 {
		t.Fatalf("expected both trackers polled concurrently, got %d updates", repo.updatedCount)
	}
}
