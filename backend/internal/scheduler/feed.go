package scheduler

import (
	"context"
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
		// loaded says trackers reflects the database: false at first and
		// again after any poll of this tick wrote to it, so a chapter one
		// site's feed just found is not polled for again on the next site's.
		loaded bool
	)

	for _, connector := range p.registry.Connectors() {
		if ctx.Err() != nil {
			break
		}
		feed, ok := connector.(connectors.UpdateFeed)
		if !ok || coolingDown(connector) {
			continue
		}
		report := FeedSiteReport{SourceKey: connector.Key()}

		fetchCtx, cancel := context.WithTimeout(ctx, feedFetchTimeout)
		rows, err := feed.RecentUpdates(fetchCtx)
		cancel()
		if ctx.Err() != nil {
			break
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

		news := p.feedNews(connector.Key(), rows)
		report.News = len(news)
		if len(news) == 0 {
			reports = append(reports, report)
			continue
		}

		var index map[string][]repository.PollingTracker
		for _, row := range news {
			if ctx.Err() != nil {
				break
			}
			if !loaded {
				trackers, err = p.repo.ListForPolling(ctx)
				if err != nil {
					p.logger.Warn("feed tick could not load trackers", "error", err)
					return append(reports, report)
				}
				loaded = true
				index = nil
			}
			if index == nil {
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
				if p.pollTracker(ctx, tracker, plan, &cycleStats{}) {
					report.Found++
				}
				loaded = false
			}
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

// feedNews returns the rows a site's feed did not carry, with that chapter,
// the last time it was read, and remembers this reading for the next one.
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
func (p *Poller) feedNews(sourceKey string, rows []connectors.SeriesUpdate) []connectors.SeriesUpdate {
	previous := p.feedSeen[sourceKey]
	current := make(map[string]float64, len(rows))
	news := make([]connectors.SeriesUpdate, 0, len(rows))
	for _, row := range rows {
		current[row.Key] = row.Chapter
		if chapter, seen := previous[row.Key]; seen && sameChapterNumber(chapter, row.Chapter) {
			continue
		}
		news = append(news, row)
	}
	p.feedSeen[sourceKey] = current
	return news
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
	return pollPlan{candidates: append(ordered, rest...), feed: sourceKey}, true
}
