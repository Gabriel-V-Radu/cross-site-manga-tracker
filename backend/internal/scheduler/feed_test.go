package scheduler

import (
	"context"
	"errors"
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
