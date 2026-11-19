package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// feedSite is a siteDouble that also publishes an update feed a test can
// change between ticks. Its feed keys are the series URLs themselves.
type feedSite struct {
	*siteDouble

	feedMu  sync.Mutex
	rows    []connectors.SeriesUpdate
	feedErr error

	reads atomic.Int32
}

func newFeedSite(key string, chapter float64) *feedSite {
	return &feedSite{siteDouble: newSite(key, chapter)}
}

func (f *feedSite) RecentUpdates(context.Context) ([]connectors.SeriesUpdate, error) {
	f.reads.Add(1)
	f.feedMu.Lock()
	defer f.feedMu.Unlock()
	if f.feedErr != nil {
		return nil, f.feedErr
	}
	return append([]connectors.SeriesUpdate(nil), f.rows...), nil
}

func (f *feedSite) FeedKey(rawURL string) (string, bool) {
	return rawURL, strings.HasPrefix(rawURL, "https://"+f.key+"/")
}

func (f *feedSite) list(rows ...connectors.SeriesUpdate) {
	f.feedMu.Lock()
	defer f.feedMu.Unlock()
	f.rows = rows
}

func (f *feedSite) setChapter(chapter float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chapter = chapterPtr(chapter)
}

func feedRow(key string, chapter float64) connectors.SeriesUpdate {
	return connectors.SeriesUpdate{Key: key, Chapter: chapter}
}

// feedTracker is a reading tracker on alpha (source 1) with beta (source 2)
// linked and in step, each under its own series path.
func feedTracker(id int64, series string, stored float64) *repository.PollingTracker {
	return &repository.PollingTracker{
		ID: id, Title: series, Status: "reading",
		SourceID: 1, SourceKey: "alpha", SourceURL: "https://alpha/" + series,
		LatestKnownChapter: chapterPtr(stored),
		AlternateSources: []repository.TrackerSourceRef{
			{SourceID: 2, SourceKey: "beta", SourceURL: "https://beta/" + series, InStep: true},
		},
	}
}

func reportFor(t *testing.T, reports []FeedSiteReport, sourceKey string) FeedSiteReport {
	t.Helper()
	for _, report := range reports {
		if report.SourceKey == sourceKey {
			return report
		}
	}
	t.Fatalf("no report for %s in %+v", sourceKey, reports)
	return FeedSiteReport{}
}

// A feed tick costs one read per site and one poll per tracker the feed shows
// a chapter beyond the stored one for; rows about series nobody tracks, or
// that the tracker already has, cost nothing. The same listing read again is
// old news.
func TestFeedPollsOnlyTheTrackersItShowsSomethingNewFor(t *testing.T) {
	repo := &statefulRepo{items: []*repository.PollingTracker{
		feedTracker(1, "one", 10),
		feedTracker(2, "two", 20),
	}}
	alpha, beta := newSite("alpha", 10), newFeedSite("beta", 11)
	beta.list(feedRow("https://beta/one", 11), feedRow("https://beta/two", 20), feedRow("https://beta/untracked", 5))
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta))

	report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.Rows != 3 || report.News != 3 || report.Polled != 1 || report.Found != 1 {
		t.Fatalf("first tick: %+v, want 3 rows, all news, one poll that found a chapter", report)
	}
	if beta.calls.Load() != 1 || alpha.calls.Load() != 0 {
		t.Fatalf("the poll goes to the site whose feed spoke, and it is trusted: beta=%d alpha=%d", beta.calls.Load(), alpha.calls.Load())
	}
	if got := *repo.items[0].LatestKnownChapter; got != 11 {
		t.Fatalf("tracker one reads %.0f, want the chapter the feed pointed at", got)
	}
	if got := *repo.items[1].LatestKnownChapter; got != 20 {
		t.Fatalf("tracker two was not ahead in the feed and must be untouched, reads %.0f", got)
	}

	report = reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.News != 0 || report.Polled != 0 || beta.calls.Load() != 1 {
		t.Fatalf("the same listing is old news: %+v, beta resolves=%d", report, beta.calls.Load())
	}

	// Series two gets a chapter: only that row is news.
	beta.setChapter(21)
	beta.list(feedRow("https://beta/one", 11), feedRow("https://beta/two", 21))
	report = reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.News != 1 || report.Polled != 1 || report.Found != 1 {
		t.Fatalf("third tick: %+v, want the one changed row polled", report)
	}
	if got := *repo.items[1].LatestKnownChapter; got != 21 {
		t.Fatalf("tracker two reads %.0f, want 21", got)
	}
}

// What the feeds are for beyond speed: a finished series is looked at every
// few days by the cycle, and a feed row reaches it at once.
func TestFeedReachesATrackerItsCadenceWouldSkip(t *testing.T) {
	tracker := feedTracker(1, "one", 10)
	tracker.Status = "completed"
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
	alpha, beta := newSite("alpha", 10), newFeedSite("beta", 11)
	beta.list(feedRow("https://beta/one", 11))
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta))
	checked := poller.now()
	tracker.LastCheckedAt = &checked

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if alpha.calls.Load()+beta.calls.Load() != 0 {
		t.Fatalf("the cycle must skip a tracker checked just now, got alpha=%d beta=%d", alpha.calls.Load(), beta.calls.Load())
	}

	report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.Polled != 1 || report.Found != 1 || *repo.items[0].LatestKnownChapter != 11 {
		t.Fatalf("the feed must reach it: %+v, tracker reads %.0f", report, *repo.items[0].LatestKnownChapter)
	}
}

// A feed row only prompts the ordinary poll, so a site that is not in step
// still cannot advance the tracker on its own word. Its claim is not asked
// about again while the listing stands, and is when the series comes back
// into the listing: it was updated again.
func TestFeedClaimFromASiteNotInStepIsHeldAndNotRepeated(t *testing.T) {
	tracker := feedTracker(1, "one", 10)
	tracker.AlternateSources[0].InStep = false
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
	alpha, beta := newSite("alpha", 10), newFeedSite("beta", 11)
	beta.list(feedRow("https://beta/one", 11))
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta))

	report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.Polled != 1 || report.Found != 0 {
		t.Fatalf("first tick: %+v, want one poll that stored nothing", report)
	}
	if beta.calls.Load() != 1 || alpha.calls.Load() != 1 {
		t.Fatalf("an untrusted claim asks the next site: beta=%d alpha=%d", beta.calls.Load(), alpha.calls.Load())
	}
	if got := *repo.items[0].LatestKnownChapter; got != 10 {
		t.Fatalf("the primary disagreed, the tracker must stay at 10, reads %.0f", got)
	}

	if report = reportFor(t, poller.RunFeeds(context.Background()), "beta"); report.Polled != 0 {
		t.Fatalf("a claim already asked about must not be repeated while the listing stands: %+v", report)
	}

	beta.list()
	poller.RunFeeds(context.Background())
	beta.list(feedRow("https://beta/one", 11))
	if report = reportFor(t, poller.RunFeeds(context.Background()), "beta"); report.Polled != 1 {
		t.Fatalf("a series back in the listing was updated again and is asked about: %+v", report)
	}
}

// One site's feed failing, or the site cooling down, costs that site's rows
// for the tick and nothing else.
func TestFeedFailureLeavesTheOtherFeedsRead(t *testing.T) {
	tracker := feedTracker(1, "one", 10)
	tracker.AlternateSources = append(tracker.AlternateSources,
		repository.TrackerSourceRef{SourceID: 3, SourceKey: "gamma", SourceURL: "https://gamma/one", InStep: true})
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}

	alpha, beta, gamma := newFeedSite("alpha", 10), newFeedSite("beta", 11), newFeedSite("gamma", 11)
	alpha.feedErr = errors.New("listing moved")
	beta.list(feedRow("https://beta/one", 11))
	gamma.list(feedRow("https://gamma/one", 11))
	gamma.cooling.Store(true)
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta, gamma))

	reports := poller.RunFeeds(context.Background())
	if report := reportFor(t, reports, "alpha"); report.Err == nil {
		t.Fatalf("alpha's failure must be reported: %+v", report)
	}
	if report := reportFor(t, reports, "beta"); report.Polled != 1 || report.Found != 1 {
		t.Fatalf("beta's feed must still be acted on: %+v", report)
	}
	if gamma.reads.Load() != 0 {
		t.Fatalf("a site cooling down must not have its feed read, got %d reads", gamma.reads.Load())
	}
}

// Both profiles tracking one series are both polled, and a chapter one site's
// feed just found is not asked for again on the next site's in the same tick.
func TestFeedPollsEveryTrackerOfASeriesOnce(t *testing.T) {
	first, second := feedTracker(1, "one", 10), feedTracker(2, "one", 10)
	second.ProfileID = 2
	repo := &statefulRepo{items: []*repository.PollingTracker{first, second}}
	alpha, beta := newFeedSite("alpha", 11), newFeedSite("beta", 11)
	alpha.list(feedRow("https://alpha/one", 11))
	beta.list(feedRow("https://beta/one", 11))
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta))

	reports := poller.RunFeeds(context.Background())
	if report := reportFor(t, reports, "alpha"); report.Polled != 2 || report.Found != 2 {
		t.Fatalf("alpha's feed must reach both trackers: %+v", report)
	}
	if report := reportFor(t, reports, "beta"); report.Polled != 0 {
		t.Fatalf("beta's feed shows nothing the trackers do not have by now: %+v", report)
	}
	if alpha.calls.Load() != 2 || beta.calls.Load() != 0 {
		t.Fatalf("expected two resolves on alpha and none on beta, got alpha=%d beta=%d", alpha.calls.Load(), beta.calls.Load())
	}
}

// The site whose feed spoke is asked first even when it never takes a turn of
// its own in the rotation.
func TestFeedAsksTheSiteThatSpokeFirstWhateverItsPlaceInTheRotation(t *testing.T) {
	tracker := &repository.PollingTracker{
		ID: 1, Title: "T", Status: "reading",
		SourceID: 1, SourceKey: "alpha", SourceURL: "https://alpha/one",
		LatestKnownChapter: chapterPtr(10),
		AlternateSources: []repository.TrackerSourceRef{
			{SourceID: 2, SourceKey: "tracking", SourceURL: "https://tracking/one", InStep: true},
		},
	}
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
	alpha, tracking := newSite("alpha", 10), newFeedSite("tracking", 11)
	tracking.trailing = true
	tracking.list(feedRow("https://tracking/one", 11))
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, tracking))

	report := reportFor(t, poller.RunFeeds(context.Background()), "tracking")
	if report.Polled != 1 || report.Found != 1 || tracking.calls.Load() != 1 || alpha.calls.Load() != 0 {
		t.Fatalf("report=%+v tracking=%d alpha=%d, want the trailing site asked first and alone",
			report, tracking.calls.Load(), alpha.calls.Load())
	}
}

// sweepingPoller is a poller with the feeds on and a two-hour sweep, whose
// clock reads what the returned function last set.
func sweepingPoller(repo pollRepository, registry *connectors.Registry) (*Poller, func(time.Time)) {
	poller := NewPoller(repo, registry, PollerConfig{
		Interval: 30 * time.Minute, FeedInterval: 5 * time.Minute, FeedSweepInterval: 2 * time.Hour,
	}, nil)
	var current atomic.Int64
	poller.now = func() time.Time { return time.Unix(0, current.Load()).UTC() }
	return poller, func(at time.Time) { current.Store(at.UnixNano()) }
}

// A tracker whose sites all publish a feed is found by the feeds, so the cycle
// only asks about it every couple of hours. Anything that leaves the feeds
// blind to it — a linked site without a feed, a feed that keeps failing, a
// chapter the feeds already missed — puts it back on every cycle.
func TestCycleOnlySweepsATrackerTheFeedsWatch(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	checked := func(ago time.Duration) *time.Time { at := now.Add(-ago); return &at }

	cases := []struct {
		name string
		// alphaHasFeed is whether the primary's site publishes a feed (beta,
		// the alternate, always does).
		alphaHasFeed bool
		checkedAgo   time.Duration
		prepare      func(poller *Poller, tracker *repository.PollingTracker)
		wantPolled   bool
	}{
		{name: "watched and checked half an hour ago: left to the feeds", alphaHasFeed: true, checkedAgo: 30 * time.Minute, wantPolled: false},
		{name: "watched and not checked for three hours: swept", alphaHasFeed: true, checkedAgo: 3 * time.Hour, wantPolled: true},
		{name: "one linked site has no feed: asked every cycle", alphaHasFeed: false, checkedAgo: 30 * time.Minute, wantPolled: true},
		{
			name: "a site's feed keeps failing: asked every cycle", alphaHasFeed: true, checkedAgo: 30 * time.Minute,
			prepare: func(poller *Poller, _ *repository.PollingTracker) {
				poller.feedFailures["beta"] = feedFailuresWorthAWarning
			},
			wantPolled: true,
		},
		{
			name: "the feeds missed its last chapter: asked every cycle", alphaHasFeed: true, checkedAgo: 30 * time.Minute,
			prepare: func(_ *Poller, tracker *repository.PollingTracker) {
				tracker.FeedMissedAt = checked(3 * 24 * time.Hour)
			},
			wantPolled: true,
		},
		{
			name: "a miss long enough ago is forgotten", alphaHasFeed: true, checkedAgo: 30 * time.Minute,
			prepare: func(_ *Poller, tracker *repository.PollingTracker) {
				tracker.FeedMissedAt = checked(feedMissMemory + time.Hour)
			},
			wantPolled: false,
		},
		{
			name: "a link the feed cannot name: asked every cycle", alphaHasFeed: true, checkedAgo: 30 * time.Minute,
			prepare: func(_ *Poller, tracker *repository.PollingTracker) {
				tracker.AlternateSources[0].SourceURL = "https://elsewhere/one"
			},
			wantPolled: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tracker := feedTracker(1, "one", 10)
			tracker.LastCheckedAt = checked(testCase.checkedAgo)
			repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}

			beta := newFeedSite("beta", 10)
			var alpha connectors.Connector = newSite("alpha", 10)
			resolves := func() int32 { return alpha.(*siteDouble).calls.Load() + beta.calls.Load() }
			if testCase.alphaHasFeed {
				feed := newFeedSite("alpha", 10)
				alpha = feed
				resolves = func() int32 { return feed.calls.Load() + beta.calls.Load() }
			}
			poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta))
			setNow(now)
			if testCase.prepare != nil {
				testCase.prepare(poller, tracker)
			}

			if err := poller.RunOnce(context.Background()); err != nil {
				t.Fatalf("run once: %v", err)
			}
			if polled := resolves() > 0; polled != testCase.wantPolled {
				t.Fatalf("polled = %v, want %v", polled, testCase.wantPolled)
			}
		})
	}

	// With the sweep turned off, or the feeds, a reading tracker is asked
	// every cycle whatever its sites publish.
	for name, cfg := range map[string]PollerConfig{
		"no sweep": {Interval: 30 * time.Minute, FeedInterval: 5 * time.Minute},
		"no feeds": {Interval: 30 * time.Minute, FeedSweepInterval: 2 * time.Hour},
	} {
		tracker := feedTracker(1, "one", 10)
		tracker.LastCheckedAt = checked(30 * time.Minute)
		alpha, beta := newFeedSite("alpha", 10), newFeedSite("beta", 10)
		poller := NewPoller(&statefulRepo{items: []*repository.PollingTracker{tracker}}, registryOf(t, alpha, beta), cfg, nil)
		poller.now = func() time.Time { return now }
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("%s: run once: %v", name, err)
		}
		if alpha.calls.Load()+beta.calls.Load() != 1 {
			t.Fatalf("%s: a reading tracker must be asked every cycle, got %d resolves", name, alpha.calls.Load()+beta.calls.Load())
		}
	}
}

// When the sweep finds a chapter that came out long before, the feeds missed
// it, and the tracker goes back to being asked every cycle. A chapter the
// sweep merely reached first, one a feed prompted, or one without a credible
// date says nothing against the feeds.
func TestSweepFindingALateChapterPutsTheTrackerBackOnEveryCycle(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	sweepFinds := func(t *testing.T, releasedAgo *time.Duration) (*statefulRepo, *Poller, func(time.Time), *feedSite, *feedSite) {
		t.Helper()
		tracker := feedTracker(1, "one", 10)
		lastChecked := now.Add(-3 * time.Hour)
		tracker.LastCheckedAt = &lastChecked
		repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
		alpha, beta := newFeedSite("alpha", 11), newFeedSite("beta", 11)
		if releasedAgo != nil {
			releasedAt := now.Add(-*releasedAgo)
			alpha.releasedAt, beta.releasedAt = &releasedAt, &releasedAt
		}
		poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta))
		setNow(now)
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run once: %v", err)
		}
		if got := *repo.items[0].LatestKnownChapter; got != 11 {
			t.Fatalf("the sweep must find chapter 11, tracker reads %.0f", got)
		}
		return repo, poller, setNow, alpha, beta
	}
	ago := func(d time.Duration) *time.Duration { return &d }

	t.Run("found two hours after it came out: the feeds missed it", func(t *testing.T) {
		repo, poller, setNow, alpha, beta := sweepFinds(t, ago(2*time.Hour))
		if repo.items[0].FeedMissedAt == nil || !repo.items[0].FeedMissedAt.Equal(now) {
			t.Fatalf("expected the miss stamped at the poll, got %v", repo.items[0].FeedMissedAt)
		}
		// Half an hour on, a watched tracker would be left to the feeds; this
		// one is asked.
		before := alpha.calls.Load() + beta.calls.Load()
		setNow(now.Add(30 * time.Minute))
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run once: %v", err)
		}
		if alpha.calls.Load()+beta.calls.Load() != before+1 {
			t.Fatal("a tracker the feeds missed must be asked every cycle again")
		}
	})

	t.Run("found ten minutes after it came out: the sweep got there first", func(t *testing.T) {
		if repo, _, _, _, _ := sweepFinds(t, ago(10*time.Minute)); repo.items[0].FeedMissedAt != nil {
			t.Fatalf("a chapter found within the grace is no miss, got %v", repo.items[0].FeedMissedAt)
		}
	})

	t.Run("dated months ago: not a date to judge the feeds by", func(t *testing.T) {
		if repo, _, _, _, _ := sweepFinds(t, ago(90*24*time.Hour)); repo.items[0].FeedMissedAt != nil {
			t.Fatalf("an old date must not count as a miss, got %v", repo.items[0].FeedMissedAt)
		}
	})

	t.Run("undated: nothing to judge by", func(t *testing.T) {
		if repo, _, _, _, _ := sweepFinds(t, nil); repo.items[0].FeedMissedAt != nil {
			t.Fatalf("an undated chapter must not count as a miss, got %v", repo.items[0].FeedMissedAt)
		}
	})

	t.Run("prompted by a feed: the feeds did their job, however late the site listed it", func(t *testing.T) {
		tracker := feedTracker(1, "one", 10)
		lastChecked := now.Add(-30 * time.Minute)
		tracker.LastCheckedAt = &lastChecked
		repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
		alpha, beta := newFeedSite("alpha", 10), newFeedSite("beta", 11)
		releasedAt := now.Add(-2 * time.Hour)
		beta.releasedAt = &releasedAt
		beta.list(feedRow("https://beta/one", 11))
		poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta))
		setNow(now)

		report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
		if report.Found != 1 {
			t.Fatalf("the feed must find the chapter: %+v", report)
		}
		if repo.items[0].FeedMissedAt != nil {
			t.Fatalf("a find the feed prompted is not a miss, got %v", repo.items[0].FeedMissedAt)
		}
	})
}

// A feed read refused locally — the site cooling down, or our own queue for it
// full — is the feed not being read, not the feed failing. Counted as
// failures, the two or three ticks inside one ten-minute cooldown declared
// the feed broken and put every tracker linked to the site back on every
// cycle, just as the site had asked for fewer requests (MangaHub on the Pi,
// 2026-11-18).
func TestFeedReadRefusedLocallyIsNotAFeedFailure(t *testing.T) {
	now := time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)

	for name, refusal := range map[string]*connectors.SourceCoolingDownError{
		"cooling down": {Host: "beta", RetryAfter: 8 * time.Minute},
		"queue full":   {Host: "beta", RetryAfter: 70 * time.Second, QueueFull: true},
	} {
		tracker := feedTracker(1, "one", 10)
		checked := now.Add(-30 * time.Minute)
		tracker.LastCheckedAt = &checked
		repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
		alpha, beta := newFeedSite("alpha", 10), newFeedSite("beta", 10)
		beta.feedErr = fmt.Errorf("read updates: %w", refusal)
		poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta))
		setNow(now)

		for tick := 0; tick <= feedFailuresWorthAWarning; tick++ {
			report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
			if report.Err == nil {
				t.Fatalf("%s: the tick must still say the feed was not read: %+v", name, report)
			}
		}
		if got := poller.feedFailures["beta"]; got != 0 {
			t.Fatalf("%s: a local refusal counted as %d feed failures", name, got)
		}
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("%s: run once: %v", name, err)
		}
		if resolves := alpha.calls.Load() + beta.calls.Load(); resolves != 0 {
			t.Fatalf("%s: the tracker must still be left to the feeds, got %d resolves", name, resolves)
		}
	}

	// A read that really fails still counts, as before.
	tracker := feedTracker(1, "one", 10)
	repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
	alpha, beta := newFeedSite("alpha", 10), newFeedSite("beta", 10)
	beta.feedErr = errors.New("listing moved")
	poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta))
	setNow(now)
	for tick := 0; tick < feedFailuresWorthAWarning; tick++ {
		poller.RunFeeds(context.Background())
	}
	if got := poller.feedFailures["beta"]; got != feedFailuresWorthAWarning {
		t.Fatalf("a failing read must count, got %d", got)
	}
}

// flakyRepo is a statefulRepo that counts how often the trackers are read and
// can fail the next reads.
type flakyRepo struct {
	*statefulRepo
	loads     atomic.Int32
	failLoads atomic.Int32
}

func (r *flakyRepo) ListForPolling(ctx context.Context) ([]repository.PollingTracker, error) {
	r.loads.Add(1)
	if r.failLoads.Load() > 0 {
		r.failLoads.Add(-1)
		return nil, errors.New("database is locked")
	}
	return r.statefulRepo.ListForPolling(ctx)
}

// A tick that cannot read the trackers must leave the feed's news news. The
// listing used to be remembered the moment it was read, so one failed
// database read turned every chapter in it into old news and left them all to
// the next cycle.
func TestFeedTickThatCannotLoadTrackersKeepsItsNews(t *testing.T) {
	repo := &flakyRepo{statefulRepo: &statefulRepo{items: []*repository.PollingTracker{feedTracker(1, "one", 10)}}}
	alpha, beta := newSite("alpha", 10), newFeedSite("beta", 11)
	beta.list(feedRow("https://beta/one", 11))
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta))

	repo.failLoads.Store(1)
	report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.News != 1 || report.Polled != 0 {
		t.Fatalf("the tick that could not load: %+v, want the news seen and nothing polled", report)
	}

	report = reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.News != 1 || report.Polled != 1 || report.Found != 1 {
		t.Fatalf("the next tick must still find the row new and act on it: %+v", report)
	}
	if got := *repo.items[0].LatestKnownChapter; got != 11 {
		t.Fatalf("tracker reads %.0f, want the chapter the feed pointed at", got)
	}

	if report = reportFor(t, poller.RunFeeds(context.Background()), "beta"); report.News != 0 {
		t.Fatalf("acted on, the listing is old news: %+v", report)
	}
}

// A tick reads the trackers once, however many chapters it finds: which
// trackers are linked to which series does not change between its polls. It
// goes back to the database only when a tracker a poll of this tick changed
// comes up again, which is what keeps the next site's feed from polling for a
// chapter this site's just found. Every poll used to send it back, before the
// next row — most often about a series nobody tracks — was even looked at.
func TestFeedTickReadsTheTrackersAgainOnlyForOneItChanged(t *testing.T) {
	repo := &flakyRepo{statefulRepo: &statefulRepo{items: []*repository.PollingTracker{
		feedTracker(1, "one", 10),
		feedTracker(2, "two", 10),
		feedTracker(3, "three", 10),
	}}}
	alpha, beta := newFeedSite("alpha", 11), newFeedSite("beta", 11)
	beta.list(
		feedRow("https://beta/one", 11), feedRow("https://beta/untracked", 3),
		feedRow("https://beta/two", 11), feedRow("https://beta/elsewhere", 7),
		feedRow("https://beta/three", 11),
	)
	poller, _ := cyclingPoller(repo, registryOf(t, alpha, beta))

	report := reportFor(t, poller.RunFeeds(context.Background()), "beta")
	if report.Polled != 3 || report.Found != 3 {
		t.Fatalf("beta's feed must reach all three trackers: %+v", report)
	}
	if got := repo.loads.Load(); got != 1 {
		t.Fatalf("three chapters on three different trackers took %d reads of the trackers, want 1", got)
	}

	// Next tick both sites list a new chapter of series one. Alpha is read
	// first and finds it; beta's row then names a tracker this tick changed.
	repo.loads.Store(0)
	alpha.setChapter(12)
	beta.setChapter(12)
	alpha.list(feedRow("https://alpha/one", 12))
	beta.list(
		feedRow("https://beta/one", 12), feedRow("https://beta/untracked", 3),
		feedRow("https://beta/two", 11), feedRow("https://beta/elsewhere", 7),
		feedRow("https://beta/three", 11),
	)
	reports := poller.RunFeeds(context.Background())
	if report := reportFor(t, reports, "alpha"); report.Polled != 1 || report.Found != 1 {
		t.Fatalf("alpha's feed finds chapter 12: %+v", report)
	}
	if report := reportFor(t, reports, "beta"); report.News != 1 || report.Polled != 0 {
		t.Fatalf("beta's feed shows nothing the tracker does not have by now: %+v", report)
	}
	if got := repo.loads.Load(); got != 2 {
		t.Fatalf("expected the one extra read for the tracker that changed, got %d reads", got)
	}
}

// Only the sites a tracker takes turns on decide whether the feeds watch it.
// An alternate past the rotation's cap is never asked in a cycle unless
// everything else fails, so its having no feed takes nothing away — but it
// used to put the tracker back on every cycle.
func TestFeedCoverageIsDecidedByTheSitesThatTakeTurns(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	polled := func(t *testing.T, prepare func(tracker *repository.PollingTracker)) bool {
		t.Helper()
		tracker := feedTracker(1, "one", 10)
		tracker.AlternateSources = append(tracker.AlternateSources,
			repository.TrackerSourceRef{SourceID: 3, SourceKey: "gamma", SourceURL: "https://gamma/one", InStep: true},
			repository.TrackerSourceRef{SourceID: 4, SourceKey: "delta", SourceURL: "https://delta/one", InStep: true},
		)
		checked := now.Add(-30 * time.Minute)
		tracker.LastCheckedAt = &checked
		if prepare != nil {
			prepare(tracker)
		}
		repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
		alpha, beta, gamma := newFeedSite("alpha", 10), newFeedSite("beta", 10), newFeedSite("gamma", 10)
		delta := newSite("delta", 10) // publishes no feed
		poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta, gamma, delta))
		setNow(now)
		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run once: %v", err)
		}
		return alpha.calls.Load()+beta.calls.Load()+gamma.calls.Load()+delta.calls.Load() > 0
	}

	if polled(t, nil) {
		t.Fatal("a fourth site that never takes a turn must not cost the tracker its coverage")
	}
	// Gamma falls out of step, so the feedless delta takes its seat: now a
	// site the tracker does take turns on has no feed.
	if !polled(t, func(tracker *repository.PollingTracker) { tracker.AlternateSources[1].InStep = false }) {
		t.Fatal("a site in the rotation without a feed leaves the tracker to the cycle")
	}
}

// A late chapter counts against the feeds only for a tracker their watch had
// slowed down. One whose own cadence is longer than the sweep is asked as
// rarely with the feeds as without them, so marking it changed nothing but
// the log, which announced it was "asked every cycle again" while it stayed
// on every twelve hours (#636, #659 and #660 on the Pi, all dropped).
func TestLateChapterOnATrackerTheFeedsDidNotSlowIsNoFeedMiss(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	for _, status := range []string{"dropped", "on_hold", "completed", "plan_to_read"} {
		tracker := feedTracker(1, "one", 10)
		tracker.Status = status
		checked := now.Add(-13 * time.Hour)
		tracker.LastCheckedAt = &checked
		repo := &statefulRepo{items: []*repository.PollingTracker{tracker}}
		alpha, beta := newFeedSite("alpha", 11), newFeedSite("beta", 11)
		releasedAt := now.Add(-2 * time.Hour)
		alpha.releasedAt, beta.releasedAt = &releasedAt, &releasedAt
		poller, setNow := sweepingPoller(repo, registryOf(t, alpha, beta))
		setNow(now)

		if err := poller.RunOnce(context.Background()); err != nil {
			t.Fatalf("%s: run once: %v", status, err)
		}
		if got := *repo.items[0].LatestKnownChapter; got != 11 {
			t.Fatalf("%s: the cycle must find chapter 11, tracker reads %.0f", status, got)
		}
		if repo.items[0].FeedMissedAt != nil {
			t.Fatalf("%s: a tracker on its own slow cadence must not be marked as missed by the feeds, got %v",
				status, repo.items[0].FeedMissedAt)
		}
	}
}

// The rest between two cycles is where the feeds are read: every feed
// interval, not on the next cycle's doorstep, and not at all when they are
// turned off.
func TestRestReadsTheFeedsBetweenCycles(t *testing.T) {
	repo := &statefulRepo{}
	site := newFeedSite("alpha", 10)

	poller := NewPoller(repo, registryOf(t, site), PollerConfig{Interval: 450 * time.Millisecond, FeedInterval: 100 * time.Millisecond}, nil)
	started := time.Now()
	if !poller.rest(context.Background()) {
		t.Fatal("a rest that ran its course must report true")
	}
	if took := time.Since(started); took < 400*time.Millisecond {
		t.Fatalf("the rest lasted %s, want the whole interval", took)
	}
	if reads := site.reads.Load(); reads < 2 || reads > 4 {
		t.Fatalf("expected the feed read every 100ms of a 450ms rest, got %d reads", reads)
	}

	site.reads.Store(0)
	poller = NewPoller(repo, registryOf(t, site), PollerConfig{Interval: 150 * time.Millisecond}, nil)
	if !poller.rest(context.Background()) || site.reads.Load() != 0 {
		t.Fatalf("with no feed interval the rest reads nothing, got %d reads", site.reads.Load())
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	poller = NewPoller(repo, registryOf(t, site), PollerConfig{Interval: time.Hour, FeedInterval: time.Minute}, nil)
	if poller.rest(cancelled) {
		t.Fatal("a rest cut short by shutdown must report false")
	}
}
