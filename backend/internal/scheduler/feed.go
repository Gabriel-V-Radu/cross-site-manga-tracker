package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// A cycle asks every due tracker's site about that one series, which is as
// many requests as there are trackers and so cannot be done often. A site's
// update feed (connectors.UpdateFeed) answers the same question for the whole
// site in one request. Between cycles the poller therefore reads the feeds
// every feedInterval and polls only the trackers a feed shows something new
// for: a chapter that used to wait for the next cycle, half an hour away at
// worst, and for its site's turn in the tracker's rotation, is found within
// one feed interval of the site listing it.
//
// A feed row only ever prompts a poll. The poll is the ordinary one
// (pollTracker), started on the site whose feed spoke, so everything that
// decides what gets stored — who may advance a number, who may take it back,
// the junk guard, the attribution — is the same code a cycle runs, and a feed
// that is wrong or approximate costs a wasted request, never a wrong chapter.
//
// The cycle stays as it is underneath: it is what finds a chapter on a site
// without a feed, one a feed's window missed, and one a feed cannot see (a
// MangaFire title whose raws run ahead of its English chapters).

// With the feeds finding the chapters, a cycle asking about a tracker they
// watch only sweeps behind them, and does so every feedSweepInterval rather
// than every cycle (cadence.go). In the feeds' first week on the Pi they found
// 106 chapters; of the 29 the cycle found on the same sites, 17 were found
// within minutes of coming out, before the next feed tick, and 8 — one in
// seventeen of those sites' chapters — were ones a feed had really missed.
//
// feedCovered decides which trackers the feeds watch, tracker by tracker, and
// errs towards asking: a tracker is covered only while every site it takes
// turns on publishes a feed that is being read and can match the tracker's
// link, and while the feeds have not recently failed that very tracker
// (feedMissMemory).

const (
	// feedMissGrace is how long after a chapter came out a cycle may find it
	// before that counts as the feeds having missed it. The slowest feed
	// (MangaFire's listing) shows a chapter a quarter of an hour late at
	// worst; half an hour leaves room for a tick or two besides.
	feedMissGrace = 30 * time.Minute
	// feedMissHorizon is the oldest release date a miss is judged by. A
	// chapter "released" weeks ago was not missed by a feed: the date is the
	// original's, or a number the site re-used.
	feedMissHorizon = 7 * 24 * time.Hour
	// feedMissMemory is how long a tracker the feeds failed goes back to
	// being asked every cycle.
	feedMissMemory = 30 * 24 * time.Hour
)

// feedsSweep reports whether the feeds are read and a sweep interval is set,
// without both of which no tracker is ever covered.
func (p *Poller) feedsSweep() bool {
	return p.feedInterval > 0 && p.feedSweepInterval > 0
}

// feedCovered reports whether the update feeds watch every site the tracker
// takes turns on, so that a cycle need only sweep behind them. rotation is
// those sites (rotationOf): the primary and the alternates under the cap.
//
// A linked site that never takes a turn does not count against the tracker —
// one with no connector, a trailing feed, and an alternate past the cap. The
// last used to: a tracker linked to three sites with feeds and a fourth
// without was asked every cycle on account of a site the cycle never asked.
func (p *Poller) feedCovered(tracker repository.PollingTracker, rotation []pollCandidate, now time.Time) bool {
	if !p.feedsSweep() || len(rotation) == 0 {
		return false
	}
	if tracker.FeedMissedAt != nil && now.Sub(*tracker.FeedMissedAt) < feedMissMemory {
		return false
	}
	for _, member := range rotation {
		if !p.feedWatches(member) {
			return false
		}
	}
	return true
}

// feedWatches reports whether one site of the rotation leaves the tracker
// covered: it publishes a feed that is being read and names this link's
// series.
func (p *Poller) feedWatches(member pollCandidate) bool {
	feed, ok := member.connector.(connectors.UpdateFeed)
	if !ok {
		return false
	}
	if _, ok := feed.FeedKey(member.ref.SourceURL); !ok {
		return false
	}
	// A feed that keeps failing watches nothing until it is read again.
	return p.feedFailures[member.connector.Key()] < feedFailuresWorthAWarning
}

// feedFetchTimeout bounds one site's feed read, pacing wait included.
const feedFetchTimeout = 60 * time.Second

// feedFailuresWorthAWarning is how many reads in a row a feed must fail
// before the log says so above Debug.
const feedFailuresWorthAWarning = 3

// FeedSiteReport is what one feed tick did with one site's feed.
type FeedSiteReport struct {
	SourceKey string
	// Err is why the feed could not be read; the other fields are then zero.
	Err error
	// Rows is how many series the feed listed, and News how many of them it
	// did not list with that chapter on the previous tick.
	Rows int
	News int
	// Polled is how many trackers the news prompted a poll of, and Found how
	// many of those polls moved a chapter up.
	Polled int
	Found  int
}

// RunFeeds reads every site's update feed once and polls the trackers a feed
// shows a chapter beyond the stored one for. It is meant to run between
// cycles, on the poller's own goroutine: it must not overlap RunOnce or
// itself.
func (p *Poller) RunFeeds(ctx context.Context) []FeedSiteReport {
	var (
		reports  []FeedSiteReport
		trackers []repository.PollingTracker
		// loaded says trackers has been read from the database this tick.
		loaded bool
		// stale are the trackers whose row no longer holds what trackers
		// says of them, because a poll of this tick changed it. One of them
		// coming up again — the next site's feed listing the chapter this
		// site's just found — is what sends the tick back to the database.
		//
		// Every poll used to, whatever it wrote: the whole library was read
		// again, and the site's index rebuilt, before the next row was even
		// looked at, though most rows are about series nobody tracks and
		// which trackers are linked to which series does not change in the
		// course of a tick.
		stale = map[int64]struct{}{}
	)
	load := func() bool {
		fresh, err := p.repo.ListForPolling(ctx)
		if err != nil {
			p.logger.Warn("feed tick could not load trackers", "error", err)
			return false
		}
		trackers, loaded = fresh, true
		clear(stale)
		return true
	}

	for _, connector := range p.registry.Connectors() {
		if ctx.Err() != nil {
			break
		}
		feed, ok := connector.(connectors.UpdateFeed)
		if !ok {
			continue
		}
		// Asked of the connector's own breaker only, not of the shared
		// circuits (coolingDown): those are per host, and the transport
		// refuses the read at once and for nothing if the host it needs is
		// the one cooling down — which the error then says, below.
		if reporter, ok := connector.(connectors.CooldownReporter); ok {
			if remaining, _ := reporter.CooldownRemaining(); remaining > 0 {
				continue
			}
		}
		report := FeedSiteReport{SourceKey: connector.Key()}

		fetchCtx, cancel := context.WithTimeout(ctx, feedFetchTimeout)
		rows, err := feed.RecentUpdates(fetchCtx)
		cancel()
		if ctx.Err() != nil {
			break
		}
		var refused *connectors.SourceCoolingDownError
		if errors.As(err, &refused) {
			// Refused locally: the site is cooling down, or this process has
			// too much queued for it. The feed was not read, which is not the
			// feed failing. Counted as that, the ten minutes of a cooldown
			// were two or three "failures" in a row — enough to declare the
			// feed broken and put every tracker linked to the site back on
			// every cycle, which is the last thing a site that has just rate
			// limited us needs (MangaHub, Pi log, 2026-11-18).
			p.logger.Debug("feed not read: refused locally", "sourceKey", connector.Key(), "error", err)
			report.Err = err
			reports = append(reports, report)
			continue
		}
		if err != nil {
			// One failed read only means this tick learned nothing from the
			// site, and the cycle's resolves are what report a site in
			// trouble. A feed that keeps failing while the site answers its
			// resolves is a different matter — its listing moved or changed
			// shape — and nothing else would ever say so.
			p.feedFailures[connector.Key()]++
			if p.feedFailures[connector.Key()] == feedFailuresWorthAWarning {
				p.logger.Warn("feed keeps failing; its trackers are back to being found by the cycle alone",
					"sourceKey", connector.Key(), "error", err)
			} else {
				p.logger.Debug("feed read failed", "sourceKey", connector.Key(), "error", err)
			}
			report.Err = err
			reports = append(reports, report)
			continue
		}
		if p.feedFailures[connector.Key()] >= feedFailuresWorthAWarning {
			p.logger.Info("feed is being read again", "sourceKey", connector.Key())
		}
		delete(p.feedFailures, connector.Key())
		report.Rows = len(rows)

		news, listing := p.feedNews(connector.Key(), rows)
		report.News = len(news)

		var index map[string][]repository.PollingTracker
		for _, row := range news {
			if ctx.Err() != nil {
				break
			}
			// A tick that cannot read the trackers stops here, and leaves
			// the listing unremembered: remembered, these rows would be old
			// news on the next tick and the chapters behind them would wait
			// for a cycle, for want of one database read.
			if !loaded && !load() {
				return append(reports, report)
			}
			if index == nil {
				index = feedIndex(trackers, connector.Key(), feed)
			}
			if anyStale(index[row.Key], stale) {
				if !load() {
					return append(reports, report)
				}
				index = feedIndex(trackers, connector.Key(), feed)
			}
			for _, tracker := range index[row.Key] {
				if tracker.LatestKnownChapter != nil && !chapterAdvances(tracker.LatestKnownChapter, row.Chapter) {
					// The feed shows nothing the tracker does not have.
					continue
				}
				plan, ok := p.planFeedPoll(tracker, connector.Key())
				if !ok {
					continue
				}
				report.Polled++
				result := p.pollTracker(ctx, tracker, plan, &cycleStats{})
				if result.found {
					report.Found++
				}
				if result.stale {
					stale[tracker.ID] = struct{}{}
				}
			}
		}
		// Not when shutdown cut the rows short: the ones it never reached
		// would be remembered as acted on.
		if ctx.Err() == nil {
			p.feedSeen[connector.Key()] = listing
		}
		if report.Polled > 0 {
			p.logger.Debug("feed prompted polls",
				"sourceKey", report.SourceKey, "rows", report.Rows, "news", report.News,
				"polled", report.Polled, "found", report.Found)
		}
		reports = append(reports, report)
	}
	return reports
}

// anyStale reports whether a poll of this tick has changed any of trackers
// since they were read.
func anyStale(trackers []repository.PollingTracker, stale map[int64]struct{}) bool {
	if len(stale) == 0 {
		return false
	}
	for _, tracker := range trackers {
		if _, ok := stale[tracker.ID]; ok {
			return true
		}
	}
	return false
}

// feedNews returns the rows a site's feed did not carry, with that chapter,
// the last time it was read, and the reading itself — series key to chapter —
// for the caller to remember (feedSeen) once it has acted on the news.
//
// What is remembered is the feed as it stood, not everything ever seen. A
// series still in the listing with the same chapter is old news and is not
// polled for again — that is what keeps a number the tracker will never store
// (a site that numbers the series higher, MangaFire's all-language count)
// from prompting a poll on every tick. One that left the listing and came
// back was updated again, whatever its number says, so it counts as news once
// more. And the memory stays the size of a feed.
//
// The first reading after a start has nothing to compare with: every row is
// news, which polls each tracker the feed shows ahead of its stored chapter
// once.
func (p *Poller) feedNews(sourceKey string, rows []connectors.SeriesUpdate) (news []connectors.SeriesUpdate, listing map[string]float64) {
	previous := p.feedSeen[sourceKey]
	listing = make(map[string]float64, len(rows))
	news = make([]connectors.SeriesUpdate, 0, len(rows))
	for _, row := range rows {
		listing[row.Key] = row.Chapter
		if chapter, seen := previous[row.Key]; seen && sameChapterNumber(chapter, row.Chapter) {
			continue
		}
		news = append(news, row)
	}
	return news, listing
}

// feedIndex groups the trackers linked to a site by the key its feed names
// their series with. A tracker linked to the site twice is listed once per
// distinct series.
func feedIndex(trackers []repository.PollingTracker, sourceKey string, feed connectors.UpdateFeed) map[string][]repository.PollingTracker {
	index := map[string][]repository.PollingTracker{}
	add := func(tracker repository.PollingTracker, linkSourceKey string, linkURL string, seen map[string]struct{}) {
		if linkSourceKey != sourceKey {
			return
		}
		key, ok := feed.FeedKey(linkURL)
		if !ok {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		index[key] = append(index[key], tracker)
	}
	for _, tracker := range trackers {
		seen := map[string]struct{}{}
		add(tracker, tracker.SourceKey, tracker.SourceURL, seen)
		for _, alternate := range tracker.AlternateSources {
			add(tracker, alternate.SourceKey, alternate.SourceURL, seen)
		}
	}
	return index
}

// planFeedPoll is the tracker's poll plan with the site whose feed spoke asked
// first, whatever its place in the rotation — a trailing feed or a site past
// the rotation's cap included, since this time it has something to say. It
// reports false when the tracker cannot be polled or has no link to that site
// the poller can ask.
func (p *Poller) planFeedPoll(tracker repository.PollingTracker, sourceKey string) (pollPlan, bool) {
	plan, ok := p.planPoll(tracker, p.cycles.Load())
	if !ok {
		return pollPlan{}, false
	}
	ordered := make([]pollCandidate, 0, len(plan.candidates))
	var rest []pollCandidate
	for _, candidate := range plan.candidates {
		if candidate.ref.SourceKey == sourceKey {
			ordered = append(ordered, candidate)
			continue
		}
		rest = append(rest, candidate)
	}
	if len(ordered) == 0 {
		return pollPlan{}, false
	}
	return pollPlan{candidates: append(ordered, rest...), feed: sourceKey, feedSwept: plan.feedSwept}, true
}
