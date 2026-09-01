package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/sourcepick"
)

// pollResolveTimeout is the per-source budget of one poll resolve, shared
// throttle wait included. 15s proved too tight for a 3.5s-gap host whenever
// anything else queued requests against it; with shards polling in parallel,
// a popular fallback host (ComicK sits under most trackers) can legitimately
// queue one request per shard, so the budget has to cover a full pacing queue
// — the throttle itself refuses anything queued past its 60s cap — plus the
// request. The poller is a background loop, so waiting longer costs nothing.
const pollResolveTimeout = 90 * time.Second

// pollRepository takes the context-carrying repository methods throughout. The
// pool runs one connection, so a statement can queue behind whoever holds it;
// the cycle's context is cancelled on shutdown, which turns "wait for the
// connection forever while the process tears down" into an error that gets
// logged.
type pollRepository interface {
	ListForPolling(ctx context.Context) ([]repository.PollingTracker, error)
	// UpdatePollingState reports false when the tracker changed after the
	// cycle snapshotted it and the write was therefore skipped.
	UpdatePollingState(ctx context.Context, update repository.PollingUpdate) (bool, error)
	MarkPollCheckedAt(ctx context.Context, trackerID int64, checkedAt time.Time) error
}

type Poller struct {
	repo                   pollRepository
	registry               *connectors.Registry
	interval               time.Duration
	idleInterval           time.Duration
	lowerConfirmationDelay time.Duration
	logger                 *slog.Logger
	stopCh                 chan struct{}
	// started records whether Start ever ran. stopCh only closes from the
	// goroutine Start launches, so a StopWait on a poller that was never
	// started (polling disabled) would otherwise sit out its whole timeout
	// waiting for a close that can never come.
	started atomic.Bool
}

type PollerConfig struct {
	Interval time.Duration
	// IdleInterval is the minimum time between polls for trackers that are
	// not in "reading" status; they rarely change, so polling them every
	// cycle just burns the sources' rate limits.
	IdleInterval time.Duration
	// LowerConfirmationDelay is how long a lower chapter number must keep being
	// reported before it replaces the stored one. It guards the two failure modes
	// against each other: a mirror that lags for one cycle never walks a tracker
	// backwards, while a genuinely wrong stored number still gets corrected
	// instead of staying frozen forever.
	LowerConfirmationDelay time.Duration
}

func NewPoller(repo pollRepository, registry *connectors.Registry, cfg PollerConfig, logger *slog.Logger) *Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Minute
	}
	if cfg.IdleInterval <= 0 {
		cfg.IdleInterval = 12 * time.Hour
	}
	if cfg.IdleInterval < cfg.Interval {
		cfg.IdleInterval = cfg.Interval
	}
	if cfg.LowerConfirmationDelay <= 0 {
		cfg.LowerConfirmationDelay = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Poller{
		repo:                   repo,
		registry:               registry,
		interval:               cfg.Interval,
		idleInterval:           cfg.IdleInterval,
		lowerConfirmationDelay: cfg.LowerConfirmationDelay,
		logger:                 logger,
		stopCh:                 make(chan struct{}),
	}
}

// Start runs poll cycles with the configured interval of rest BETWEEN them,
// measured from the end of one cycle to the start of the next. A fixed ticker
// would fire mid-cycle whenever a cycle outgrows the interval — a full
// library against throttled hosts takes longer than 30 minutes — and the
// pending tick would start the next cycle immediately, turning the poller
// into a rest-less loop that keeps every host's pacing queue permanently
// busy and user-facing requests permanently behind it.
func (p *Poller) Start(ctx context.Context) {
	p.started.Store(true)
	p.logger.Info("poller started", "interval", p.interval.String())
	go func() {
		for {
			if err := p.RunOnce(ctx); err != nil {
				p.logger.Warn("poller cycle failed", "error", err)
			}
			select {
			case <-ctx.Done():
				p.logger.Info("poller stopped")
				close(p.stopCh)
				return
			case <-time.After(p.interval):
			}
		}
	}()
}

func (p *Poller) StopWait(timeout time.Duration) {
	if !p.started.Load() {
		// Start never ran, so there is no goroutine to wait for and nobody to
		// ever close stopCh; waiting would just burn the whole timeout.
		return
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	select {
	case <-p.stopCh:
	case <-time.After(timeout):
	}
}

// cycleStats accumulates one cycle's summary counters across the per-source
// shard goroutines.
type cycleStats struct {
	skippedIdle     atomic.Int64
	coolingResolves atomic.Int64
}

// RunOnce polls every due tracker. Trackers are sharded by their primary
// source and each shard runs on its own goroutine: the shared transport
// throttle paces requests per host and no two connectors share a host, so
// serializing different sources against each other bought nothing — it made
// the cycle as long as the sum of every host's waits instead of the slowest
// one's. Within a shard trackers stay sequential, which keeps each host's
// pacing queue at depth one and so clear of the throttle's queue-wait cap.
// Fallback resolves do cross into other shards' hosts; the throttle paces
// those too, which is why pollResolveTimeout budgets for a full queue.
func (p *Poller) RunOnce(ctx context.Context) error {
	trackers, err := p.repo.ListForPolling(ctx)
	if err != nil {
		return fmt.Errorf("load trackers for polling: %w", err)
	}

	shards := map[string][]repository.PollingTracker{}
	for _, tracker := range trackers {
		shards[tracker.SourceKey] = append(shards[tracker.SourceKey], tracker)
	}

	var stats cycleStats
	var wg sync.WaitGroup
	for key, shard := range shards {
		wg.Add(1)
		go func(key string, shard []repository.PollingTracker) {
			defer wg.Done()
			started := time.Now()
			for _, tracker := range shard {
				// A cancelled context fails every remaining resolve anyway;
				// stopping here just skips the doomed churn on shutdown.
				if ctx.Err() != nil {
					return
				}
				p.pollTracker(ctx, tracker, &stats)
			}
			p.logger.Debug("poll shard finished",
				"sourceKey", key,
				"trackers", len(shard),
				"took", time.Since(started).Round(time.Millisecond).String())
		}(key, shard)
	}
	wg.Wait()

	if skipped := stats.skippedIdle.Load(); skipped > 0 {
		p.logger.Debug("poll skipped idle trackers", "count", skipped)
	}
	if cooling := stats.coolingResolves.Load(); cooling > 0 {
		p.logger.Info("poll cycle hit cooling-down sources", "resolvesRefused", cooling)
	}

	return nil
}

// pollTracker runs one tracker's poll end to end: resolve (with fallback),
// reconcile the reported chapter, persist. It is the body RunOnce used to
// inline; each shard goroutine calls it for its own trackers only, so
// everything here that touches shared state (repo, registry, throttle) must
// stay goroutine-safe.
func (p *Poller) pollTracker(ctx context.Context, tracker repository.PollingTracker, stats *cycleStats) {
	if p.shouldSkipIdle(tracker) {
		stats.skippedIdle.Add(1)
		return
	}

	connector, ok := p.registry.Get(tracker.SourceKey)
	if !ok {
		p.logger.Debug("connector missing for tracker", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey)
		return
	}

	result, resolveErr := p.resolveSource(ctx, connector, tracker.SourceURL)

	// A source can go dark for reasons no retry fixes — a site put behind an
	// interactive bot challenge, a domain that moved. When that happens, poll
	// the tracker's other linked sources so the chapter count keeps advancing
	// from whichever mirror is still readable.
	usedFallback := false
	reporterSourceID := tracker.SourceID
	if resolveErr != nil {
		// Shutdown cancelled the cycle mid-resolve. The error says nothing about
		// the source, the alternates would fail the same way, and stamping
		// last_checked_at with a dead context only adds a second warning per
		// in-flight tracker to the shutdown log.
		if ctx.Err() != nil {
			return
		}
		if fallbackResult, fallbackSource := p.resolveFromAlternates(ctx, tracker); fallbackResult != nil {
			p.logger.Info("poll fell back to alternate source",
				"trackerId", tracker.ID,
				"primarySourceKey", tracker.SourceKey,
				"fallbackSourceKey", fallbackSource.SourceKey,
				"primaryError", resolveErr)
			result, resolveErr = fallbackResult, nil
			usedFallback = true
			reporterSourceID = fallbackSource.SourceID
		}
	}

	if resolveErr != nil {
		// A cooling-down source is a known, already-logged outage; one Warn
		// per tracker per cycle for it is noise, and the cycle summary
		// reports the count. Anything else is worth a line of its own.
		var cooling *connectors.SourceCoolingDownError
		if errors.As(resolveErr, &cooling) {
			stats.coolingResolves.Add(1)
			p.logger.Debug("poll resolve skipped: source cooling down", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey, "error", resolveErr)
		} else {
			p.logger.Warn("poll resolve failed", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey, "error", resolveErr)
		}
		// A failed check is still a check. Without stamping it, a non-reading
		// tracker whose sources are all dark keeps last_checked_at frozen, so
		// shouldSkipIdle can never defer it and the poller retries it in full
		// every cycle for as long as the outage lasts.
		if err := p.repo.MarkPollCheckedAt(ctx, tracker.ID, time.Now().UTC()); err != nil {
			p.logger.Warn("poll mark checked failed", "trackerId", tracker.ID, "error", err)
		}
		return
	}

	// A corrupt number from any single source must not poison the stored
	// chapter: once stored, the same source re-confirms it every cycle and
	// the pending-lower path can never walk it back. Fallback candidates
	// are filtered inside resolveFromAlternates; this catches the primary.
	if result.LatestChapter != nil && implausibleChapterAdvance(tracker.LatestKnownChapter, *result.LatestChapter) {
		p.logger.Warn("poll reported an implausible chapter jump; ignoring the number",
			"trackerId", tracker.ID,
			"title", tracker.Title,
			"sourceKey", tracker.SourceKey,
			"stored", derefChapter(tracker.LatestKnownChapter),
			"reported", *result.LatestChapter,
			"usedFallback", usedFallback)
		result.LatestChapter = nil
	}

	now := time.Now().UTC()
	outcome := decideChapter(tracker, result.LatestChapter, usedFallback, now, p.lowerConfirmationDelay)
	latest := outcome.latest
	if outcome.corrected {
		p.logger.Info("poll corrected the stored chapter downwards",
			"trackerId", tracker.ID,
			"title", tracker.Title,
			"from", derefChapter(tracker.LatestKnownChapter),
			"to", derefChapter(latest),
			"sourceKey", tracker.SourceKey,
			"usedFallback", usedFallback)
	}

	// The source whose report the stored number now reflects. Recorded when
	// this poll's report IS the stored number — an advance, a correction, a
	// first fill, or a plain confirmation (which backfills trackers from
	// before the column existed and keeps the attribution current). A poll
	// whose report lost the reconciliation — a lagging mirror, a pending
	// lower — must not claim a number it did not supply.
	var latestChapterSourceID *int64
	if reporterSourceID > 0 && latest != nil && result.LatestChapter != nil &&
		sameChapterNumber(*latest, *result.LatestChapter) {
		latestChapterSourceID = &reporterSourceID
	}

	// A correction counts here as much as an advance: when the recorded
	// chapter changes at all the stored date belongs to the chapter that was
	// replaced, so it has to be rewritten. Comparing against `latest` rather
	// than the raw result keeps the fallback rule above intact — a mirror
	// that merely lags leaves `latest` alone and so reads as no change.
	chapterMoved := chapterNumberChanged(tracker.LatestKnownChapter, latest)
	latestReleaseAt := result.LastUpdatedAt
	if !chapterMoved && tracker.LatestReleaseAt != nil {
		// The chapter didn't move, so keep the stored release date
		// rather than rewriting it with the source's current value —
		// some sources bump their update timestamp without releasing
		// anything, which made dates drift toward "just now".
		latestReleaseAt = nil
	}
	// chapterMoved, not newChapter: a correction downward changes the recorded
	// chapter just as much as an advance does, and leaves the stored date
	// describing a chapter that is no longer there. Gating on the advance-only
	// test left that stale date on the card, next to a number it never belonged
	// to, and the default ordering sorts by it.
	clearLatestReleaseAt := latestReleaseAt == nil && chapterMoved
	if usedFallback {
		// A fallback source may know nothing about release dates (MangaBuddy
		// reports none usable), and dropping to one must never destroy what the
		// primary already recorded. Keeping a stale date beside a newer chapter
		// is imperfect; erasing 355 series' dates is not a trade worth making.
		clearLatestReleaseAt = false
	}

	var canonicalSourceItemID *string
	resolvedSourceItemID := strings.TrimSpace(result.SourceItemID)
	if resolvedSourceItemID != "" {
		canonicalSourceItemID = &resolvedSourceItemID
	} else {
		canonicalSourceItemID = tracker.SourceItemID
	}
	canonicalSourceURL := strings.TrimSpace(result.URL)
	if canonicalSourceURL == "" {
		canonicalSourceURL = tracker.SourceURL
	}
	sourceID := tracker.SourceID
	currentSourceURL := tracker.SourceURL

	if usedFallback {
		// The chapter number carries across sources; the identifiers do not.
		// Writing the fallback's id/URL here would silently repoint the
		// tracker's primary source at the mirror while source_id still names
		// the original, so a fallback poll updates progress only and leaves
		// the primary pointer for the user to change deliberately.
		canonicalSourceItemID = nil
		canonicalSourceURL = ""
		sourceID = 0
		currentSourceURL = ""
	}

	applied, err := p.repo.UpdatePollingState(ctx, repository.PollingUpdate{
		TrackerID:             tracker.ID,
		SnapshotSourceID:      tracker.SourceID,
		SourceID:              sourceID,
		CurrentSourceURL:      currentSourceURL,
		SourceItemID:          canonicalSourceItemID,
		SourceURL:             canonicalSourceURL,
		LatestKnownChapter:    latest,
		LatestChapterSourceID: latestChapterSourceID,
		LatestReleaseAt:       latestReleaseAt,
		ClearLatestReleaseAt:  clearLatestReleaseAt,
		PendingLowerChapter:   outcome.pendingLower,
		CheckedAt:             now,
	})
	if err != nil {
		p.logger.Warn("poll update state failed", "trackerId", tracker.ID, "error", err)
	} else if !applied {
		p.logger.Debug("poll update skipped: tracker changed mid-cycle", "trackerId", tracker.ID, "snapshotSourceId", tracker.SourceID)
	}
}

// resolveSource asks a connector for a series — unless the connector already
// declared itself cooling down, in which case the call is refused locally.
// The connector would fail such a request fast anyway; asking first spares
// the doomed round through URL parsing and the signer, and returns the same
// SourceCoolingDownError the shared throttle uses, so the caller classifies
// both breakers identically.
func (p *Poller) resolveSource(ctx context.Context, connector connectors.Connector, rawURL string) (*connectors.MangaResult, error) {
	if reporter, ok := connector.(connectors.CooldownReporter); ok {
		if remaining, reason := reporter.CooldownRemaining(); remaining > 0 {
			return nil, fmt.Errorf("%s: %w", reason, &connectors.SourceCoolingDownError{
				Host:       connector.Key(),
				RetryAfter: remaining,
			})
		}
	}

	// The budget covers the shared throttle's slot wait too: a host with a
	// widened gap (api.comick.dev at 3.5s) plus a burst of dashboard-driven
	// requests can queue a resolve for >15s, and a poll that gives up then
	// hands the answer to a staler mirror. The poller is a background loop,
	// so waiting longer costs nothing.
	requestCtx, cancel := context.WithTimeout(ctx, pollResolveTimeout)
	defer cancel()
	return connector.ResolveByURL(requestCtx, rawURL)
}

// chapterOutcome is what a poll decides to record for a tracker's chapter
// number: the value to store, any lower value still awaiting confirmation, and
// whether the stored number was walked backwards.
type chapterOutcome struct {
	latest       *float64
	pendingLower *float64
	corrected    bool
}

// decideChapter reconciles the number a poll reported with the one on record.
//
// A primary source is authoritative: whatever it reports is stored, corrections
// downwards included, which is how a source that used to over-report gets to fix
// its own history. A fallback is not — it may be a mirror that simply lags — so a
// lower number from one is only applied once it has kept reporting the same value
// for confirmationDelay. Until then it is remembered, not acted on.
//
// Both paths refuse to record a chapter below the one the reader has already
// finished, so no correction can make read chapters look unread.
func decideChapter(
	tracker repository.PollingTracker,
	reported *float64,
	usedFallback bool,
	now time.Time,
	confirmationDelay time.Duration,
) chapterOutcome {
	stored := tracker.LatestKnownChapter

	// Nothing was reported, so there is nothing to decide and nothing to keep
	// waiting on.
	if reported == nil {
		return chapterOutcome{latest: stored}
	}
	if stored == nil {
		return chapterOutcome{latest: reported}
	}

	if *reported > *stored || sameChapterNumber(*reported, *stored) {
		// An advance, or agreement. Either way any pending correction is stale.
		if *reported > *stored {
			return chapterOutcome{latest: reported}
		}
		return chapterOutcome{latest: stored}
	}

	// The report is lower than the stored number.
	candidate := *reported
	if tracker.LastReadChapter != nil && candidate < *tracker.LastReadChapter {
		candidate = *tracker.LastReadChapter
	}
	if candidate >= *stored {
		// Clamping to the read position swallowed the whole correction.
		return chapterOutcome{latest: stored}
	}

	if !usedFallback {
		return chapterOutcome{latest: &candidate, corrected: true}
	}

	confirmed := tracker.PendingLowerChapter != nil &&
		sameChapterNumber(*tracker.PendingLowerChapter, *reported) &&
		tracker.PendingLowerFirstSeenAt != nil &&
		!now.Before(tracker.PendingLowerFirstSeenAt.Add(confirmationDelay))

	if confirmed {
		return chapterOutcome{latest: &candidate, corrected: true}
	}

	return chapterOutcome{latest: stored, pendingLower: reported}
}

// implausibleChapterAdvance reports whether a reported chapter number is too
// far above the stored one to be a real release. Sources have been observed
// reporting junk numbers — years ("Chapter 2019"), numbers lifted from series
// titles ("LVL 9999"), placeholder entries ("Chapter 1000" on a run of 249) —
// and the highest-number-wins reconciliation stores such a value permanently:
// every later cycle re-confirms it, so update detection freezes. The margin is
// far above the largest legitimate divergence seen between sources (~1.7x from
// cross-language chapter inflation), and very young trackers are exempt
// because a freshly linked series can genuinely catch up by that much.
func implausibleChapterAdvance(stored *float64, reported float64) bool {
	if stored == nil || *stored < 10 {
		return false
	}
	return reported > *stored*2+100
}

func derefChapter(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// resolveFromAlternates consults every one of the tracker's non-primary linked
// sources and returns the best answer: the highest chapter number, breaking
// ties in favour of a result that carries a release date. Taking the first
// source that answered used to let a mirror that is merely alive-but-stale
// (MangaBuddy lags real releases by chapters) shadow a fresher one linked
// later (MangaUpdates). It returns (nil, zero) when the tracker has no
// alternates or none of them answer, leaving the caller to report the primary
// source's error.
func (p *Poller) resolveFromAlternates(ctx context.Context, tracker repository.PollingTracker) (*connectors.MangaResult, repository.TrackerSourceRef) {
	var best *connectors.MangaResult
	var bestSource repository.TrackerSourceRef

	for _, source := range tracker.AlternateSources {
		if strings.TrimSpace(source.SourceURL) == "" {
			continue
		}

		connector, ok := p.registry.Get(source.SourceKey)
		if !ok {
			continue
		}

		result, err := p.resolveSource(ctx, connector, source.SourceURL)
		if err != nil {
			p.logger.Debug("poll fallback source failed",
				"trackerId", tracker.ID, "sourceKey", source.SourceKey, "error", err)
			continue
		}
		if result == nil {
			continue
		}

		// Drop an implausible number here rather than after picking the best:
		// one source's junk answer would otherwise shadow a sane advance from
		// another mirror, since the highest number wins the comparison below.
		if result.LatestChapter != nil && implausibleChapterAdvance(tracker.LatestKnownChapter, *result.LatestChapter) {
			p.logger.Warn("fallback source reported an implausible chapter jump; ignoring the number",
				"trackerId", tracker.ID,
				"title", tracker.Title,
				"sourceKey", source.SourceKey,
				"stored", derefChapter(tracker.LatestKnownChapter),
				"reported", *result.LatestChapter)
			result.LatestChapter = nil
		}

		if betterFallbackResult(best, result) {
			best = result
			bestSource = source
		}
	}

	return best, bestSource
}

// betterFallbackResult decides whether candidate should replace current as the
// fallback answer. Ranking two answers is the shared best-source rule; "any
// answer at all beats none" is this caller's own, because a fallback that
// resolved is also what tells the poll the tracker was reached — even when the
// site had no chapter number to give.
func betterFallbackResult(current *connectors.MangaResult, candidate *connectors.MangaResult) bool {
	if current == nil {
		return true
	}
	return sourcepick.Better(readingOf(current), readingOf(candidate))
}

func readingOf(result *connectors.MangaResult) sourcepick.Reading {
	if result == nil {
		return sourcepick.Reading{}
	}
	return sourcepick.Reading{Chapter: result.LatestChapter, ReleaseAt: result.LastUpdatedAt}
}

// shouldSkipIdle reports whether a non-reading tracker was checked recently
// enough that this cycle can skip it.
func (p *Poller) shouldSkipIdle(tracker repository.PollingTracker) bool {
	if strings.EqualFold(strings.TrimSpace(tracker.Status), "reading") {
		return false
	}
	if tracker.LastCheckedAt == nil {
		return false
	}
	return time.Since(*tracker.LastCheckedAt) < p.idleInterval
}

// sameChapterNumber compares two chapter numbers with a tolerance. The pending
// correction is compared against a value that has round-tripped through JSON and
// a SQLite REAL column since it was first seen, so exact equality is the wrong
// test for deciding whether a source is still reporting the same thing.
func sameChapterNumber(a float64, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// chapterNumberChanged reports whether the recorded latest chapter differs from
// what it was, in either direction. A drop is not noise to be ignored: it is how
// a source that was reporting the wrong number reports the right one.
func chapterNumberChanged(previous *float64, current *float64) bool {
	if previous == nil && current == nil {
		return false
	}
	if previous == nil || current == nil {
		return true
	}
	return *previous != *current
}
