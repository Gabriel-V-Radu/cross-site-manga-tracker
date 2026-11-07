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
	MarkPollCheckedAt(ctx context.Context, trackerID int64, checkedAt time.Time, polls []repository.SourcePoll) error
}

type Poller struct {
	repo                   pollRepository
	registry               *connectors.Registry
	interval               time.Duration
	idleInterval           time.Duration
	lowerConfirmationDelay time.Duration
	logger                 *slog.Logger
	stopCh                 chan struct{}
	// now stamps each poll's writes, last_polled_at included, which is what
	// orders the next cycle's rotation; tests replace it to step through
	// cycles deterministically.
	now func() time.Time
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
		now:                    time.Now,
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
	p.registry.HealthBoard().PollerStarted(p.interval)
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

// plannedPoll is one tracker due this cycle, with the order its poll asks its
// linked sites in.
type plannedPoll struct {
	tracker repository.PollingTracker
	plan    pollPlan
}

// RunOnce polls every due tracker. Each tracker's poll is planned first (see
// planPoll: whose turn it is among its linked sites), and polls are sharded
// by the site their first request goes to, each shard on its own goroutine:
// the shared transport throttle paces requests per host and no two connectors
// share a host, so serializing different sources against each other bought
// nothing — it made the cycle as long as the sum of every host's waits
// instead of the slowest one's. Within a shard polls stay sequential, which
// keeps each host's pacing queue at depth one and so clear of the throttle's
// queue-wait cap. A poll that walks on past its turn crosses into another
// shard's host; the throttle paces those too, which is why pollResolveTimeout
// budgets for a full queue.
//
// Sharding by the turn rather than by the primary source is what lets the
// rotation shorten the cycle: the library's MangaFire trackers used to queue
// one after another behind MangaFire's pacing gap while the other hosts sat
// idle.
//
// Every outcome is also filed on the registry's health board, which is what
// the dashboard's source health panel reads; the cycle is folded into its
// verdicts only once every shard is done.
func (p *Poller) RunOnce(ctx context.Context) error {
	board := p.registry.HealthBoard()
	board.BeginCycle()
	trackers, err := p.repo.ListForPolling(ctx)
	if err != nil {
		err = fmt.Errorf("load trackers for polling: %w", err)
		board.CycleFailed(err)
		return err
	}

	var stats cycleStats
	shards := map[string][]plannedPoll{}
	for _, tracker := range trackers {
		if p.shouldSkipIdle(tracker) {
			stats.skippedIdle.Add(1)
			continue
		}
		plan, ok := p.planPoll(tracker)
		if !ok {
			// Silent in the log on purpose (one line per tracker per cycle),
			// but not on the board: a tracker on a retired site is never
			// checked again, and only the panel can tell the reader to relink
			// it.
			p.logger.Debug("connector missing for tracker", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey)
			board.RecordMissingConnector(tracker.SourceKey, seriesOf(tracker))
			continue
		}
		key := plan.shardKey()
		shards[key] = append(shards[key], plannedPoll{tracker: tracker, plan: plan})
	}

	var wg sync.WaitGroup
	for key, shard := range shards {
		wg.Add(1)
		go func(key string, shard []plannedPoll) {
			defer wg.Done()
			started := time.Now()
			for _, planned := range shard {
				// A cancelled context fails every remaining resolve anyway;
				// stopping here just skips the doomed churn on shutdown.
				if ctx.Err() != nil {
					return
				}
				p.pollTracker(ctx, planned.tracker, planned.plan, &stats)
			}
			p.logger.Debug("poll shard finished",
				"sourceKey", key,
				"trackers", len(shard),
				"took", time.Since(started).Round(time.Millisecond).String())
		}(key, shard)
	}
	wg.Wait()

	if ctx.Err() != nil {
		// Shutdown cut the cycle short; its cancelled resolves would read as
		// every site failing at once.
		board.AbortCycle()
	} else {
		board.EndCycle()
	}

	if skipped := stats.skippedIdle.Load(); skipped > 0 {
		p.logger.Debug("poll skipped idle trackers", "count", skipped)
	}
	if cooling := stats.coolingResolves.Load(); cooling > 0 {
		p.logger.Info("poll cycle hit cooling-down sources", "resolvesRefused", cooling)
	}

	return nil
}

func seriesOf(tracker repository.PollingTracker) connectors.TrackedSeries {
	return connectors.TrackedSeries{ID: tracker.ID, Title: tracker.Title, ProfileID: tracker.ProfileID}
}

// pollAnswer is one linked site's successful answer during a poll.
type pollAnswer struct {
	candidate pollCandidate
	result    *connectors.MangaResult
}

// pollTracker runs one tracker's poll end to end: ask its linked sites in the
// plan's order until one gives a usable answer, reconcile the reported
// chapter, persist. Each shard goroutine calls it for its own trackers only,
// so everything here that touches shared state (repo, registry, throttle)
// must stay goroutine-safe.
//
// The walk stops at the first site that answers with a chapter number it may
// report, which in the common case is the first site asked: one request per
// tracker per cycle. It walks on past a site that fails or is cooling down —
// a source can go dark for reasons no retry fixes, a site put behind an
// interactive bot challenge, a domain that moved — past one that answers
// without a number (MangaUpdates past its staleness cutoff, a feed with no
// English chapters, a scraper whose markup stopped matching, a number dropped
// as junk), and past an alternate that claims a new chapter while not in step
// (see pollCandidate.trusted). That claim stands at once when it is dated well
// after the stored chapter (releasedWellAfter) or when the next site reports
// the same number; otherwise it is held, and stands only if no site after it
// answers at all.
func (p *Poller) pollTracker(ctx context.Context, tracker repository.PollingTracker, plan pollPlan, stats *cycleStats) {
	board := p.registry.HealthBoard()

	var (
		// answer is the report this poll goes by; held, the best
		// unconfirmed advance from an alternate not in step; firstAnswer,
		// the first site that answered at all, number or not.
		answer, held, firstAnswer *pollAnswer
		// unconfirmed are every held advance so far, which a later site
		// reporting the same number confirms.
		unconfirmed []pollAnswer
		// primaryResult is the primary's answer when it was asked and
		// answered: only then are its canonical identifiers refreshed.
		primaryResult *connectors.MangaResult
		// turn is the site whose turn this poll was: the first one asked,
		// or refused locally.
		turn    *pollCandidate
		turnErr error
		// polls records every site that was actually reached, for the
		// rotation's memory.
		answered []pollAnswer
		failed   []pollCandidate
	)

	for index := range plan.candidates {
		candidate := plan.candidates[index]
		role := connectors.RoleFallback
		if turn == nil {
			role = connectors.RoleTurn
		}

		result, err := p.resolveSource(ctx, candidate.connector, candidate.ref.SourceURL)
		// Shutdown cancelled the cycle mid-resolve. The error says nothing
		// about the site, the others would fail the same way, and stamping
		// last_checked_at with a dead context only adds a warning per
		// in-flight tracker to the shutdown log.
		if ctx.Err() != nil {
			return
		}
		if err == nil && result == nil {
			err = errors.New("source returned no result")
		}
		board.RecordResolve(candidate.ref.SourceKey, role, result, err)
		if turn == nil {
			turn, turnErr = &plan.candidates[index], err
		}

		if err != nil {
			var cooling *connectors.SourceCoolingDownError
			if errors.As(err, &cooling) {
				// Refused locally: the site was never reached, so this does
				// not use up its turn.
				stats.coolingResolves.Add(1)
			} else {
				failed = append(failed, candidate)
			}
			p.logger.Debug("poll source failed",
				"trackerId", tracker.ID, "sourceKey", candidate.ref.SourceKey, "error", err)
			continue
		}

		result = p.dropImplausibleChapter(tracker, candidate, result)
		current := pollAnswer{candidate: candidate, result: result}
		answered = append(answered, current)
		if candidate.primary {
			primaryResult = result
		}
		if firstAnswer == nil {
			firstAnswer = &current
		}

		chapter := result.LatestChapter
		if chapter == nil {
			continue
		}
		if !candidate.trusted && chapterAdvances(tracker.LatestKnownChapter, *chapter) {
			if corroborating := sameAdvance(unconfirmed, *chapter); corroborating != nil {
				// A second site reports the same new chapter: that is
				// agreement, not one site's numbering. The first one asked
				// keeps the report unless only this one dates it.
				answer = corroborating
				if betterFallbackResult(corroborating.result, result) {
					answer = &current
				}
				break
			}
			if releasedWellAfter(tracker.LatestReleaseAt, result.LastUpdatedAt) {
				// Dated well after the stored chapter: a release that came
				// later, not the stored one under another number.
				answer = &current
				break
			}
			unconfirmed = append(unconfirmed, current)
			if held == nil || betterFallbackResult(held.result, result) {
				held = &current
			}
			continue
		}
		answer = &current
		break
	}
	if answer == nil {
		// Nothing trusted spoke up, so an unconfirmed advance is the best
		// there is — the same call the fallback has always made when the
		// primary was dark.
		answer = held
	}

	series := seriesOf(tracker)
	var turnCooling *connectors.SourceCoolingDownError
	turnWasCooling := errors.As(turnErr, &turnCooling)
	if turnErr != nil {
		board.RecordTurnFailure(turn.ref.SourceKey, series, firstAnswer != nil)
		if firstAnswer != nil && !turnWasCooling {
			p.logger.Info("poll fell back to another source",
				"trackerId", tracker.ID,
				"turnSourceKey", turn.ref.SourceKey,
				"fallbackSourceKey", firstAnswer.candidate.ref.SourceKey,
				"turnError", turnErr)
		}
	}

	now := p.now().UTC()
	if firstAnswer == nil {
		// A cooling-down source is a known, already-logged outage; one Warn
		// per tracker per cycle for it is noise, and the cycle summary
		// reports the count. Anything else is worth a line of its own.
		if turnWasCooling {
			p.logger.Debug("poll resolve skipped: source cooling down", "trackerId", tracker.ID, "sourceKey", turn.ref.SourceKey, "error", turnErr)
		} else {
			p.logger.Warn("poll resolve failed", "trackerId", tracker.ID, "sourceKey", turn.ref.SourceKey, "error", turnErr, "sitesTried", len(plan.candidates))
		}
		// A failed check is still a check. Without stamping it, a non-reading
		// tracker whose sources are all dark keeps last_checked_at frozen, so
		// shouldSkipIdle can never defer it and the poller retries it in full
		// every cycle for as long as the outage lasts.
		if err := p.repo.MarkPollCheckedAt(ctx, tracker.ID, now, sourcePolls(nil, failed, nil)); err != nil {
			p.logger.Warn("poll mark checked failed", "trackerId", tracker.ID, "error", err)
		}
		return
	}

	var reported *float64
	var reportedReleaseAt *time.Time
	chapterFromAlternate := false
	var reporterSourceID int64
	if answer != nil {
		reported = answer.result.LatestChapter
		reportedReleaseAt = answer.result.LastUpdatedAt
		chapterFromAlternate = !answer.candidate.primary
		reporterSourceID = answer.candidate.ref.SourceID
		if turn != nil && answer.candidate.ref.SourceKey != turn.ref.SourceKey {
			p.logger.Debug("poll took the chapter from a site past its turn",
				"trackerId", tracker.ID,
				"turnSourceKey", turn.ref.SourceKey,
				"reporterSourceKey", answer.candidate.ref.SourceKey,
				"reported", *reported)
		}
	}

	outcome := decideChapter(tracker, reported, chapterFromAlternate, now, p.lowerConfirmationDelay)
	latest := outcome.latest
	if outcome.corrected {
		p.logger.Info("poll corrected the stored chapter downwards",
			"trackerId", tracker.ID,
			"title", tracker.Title,
			"from", derefChapter(tracker.LatestKnownChapter),
			"to", derefChapter(latest),
			"sourceKey", answer.candidate.ref.SourceKey,
			"fromAlternate", chapterFromAlternate)
	}

	// Whether this poll's report IS the stored number — an advance, a
	// correction, a first fill, or a plain confirmation — as opposed to a
	// report that lost the reconciliation: a lagging mirror, a pending lower,
	// a correction clamped at the read position, or no number at all.
	reportIsStored := latest != nil && reported != nil && sameChapterNumber(*latest, *reported)

	// The source whose report put the stored number there. With the poll
	// rotating, every linked site confirms the number in turn; letting each
	// confirmation take over the attribution would hand it to a different
	// site every cycle, and with it the primary's right to correct the number
	// at once (decideChapter). So the site that found the number keeps it,
	// and a plain confirmation only fills an attribution that is missing. A
	// poll whose report lost the reconciliation must not claim a number it
	// did not supply.
	var latestChapterSourceID *int64
	if reporterSourceID > 0 && reportIsStored &&
		(chapterNumberChanged(tracker.LatestKnownChapter, latest) || tracker.LatestChapterSourceID == nil) {
		latestChapterSourceID = &reporterSourceID
	}

	// A correction counts here as much as an advance: when the recorded
	// chapter changes at all the stored date belongs to the chapter that was
	// replaced, so it has to be rewritten. Comparing against `latest` rather
	// than the raw report keeps the lagging-mirror rule intact — a mirror
	// that merely lags leaves `latest` alone and so reads as no change.
	chapterMoved := chapterNumberChanged(tracker.LatestKnownChapter, latest)
	latestReleaseAt := reportedReleaseAt
	if !chapterMoved && (tracker.LatestReleaseAt != nil || !reportIsStored) {
		// The chapter didn't move, so keep the stored release date rather
		// than rewriting it with the source's current value — some sources
		// bump their update timestamp without releasing anything, which made
		// dates drift toward "just now". A missing date is filled only from a
		// report of the stored chapter itself: a lagging mirror's lower
		// chapter carries a date that belongs to some other chapter, and once
		// written it would sit beside the stored one as if exact until the
		// next release.
		latestReleaseAt = nil
	}
	// chapterMoved, not an advance-only test: a correction downward changes
	// the recorded chapter just as much as an advance does, and leaves the
	// stored date describing a chapter that is no longer there.
	clearLatestReleaseAt := latestReleaseAt == nil && chapterMoved
	if chapterFromAlternate {
		// An alternate may know nothing about release dates, and its number
		// standing in must never destroy what the primary already recorded.
		// Keeping a stale date beside a newer chapter is imperfect; erasing
		// a library's dates is not a trade worth making.
		clearLatestReleaseAt = false
	}

	// The chapter number carries across sources; the identifiers do not. Only
	// a poll the primary answered refreshes the primary's canonical id and
	// URL and its tracker_sources mirror. Writing an alternate's id/URL here
	// would silently repoint the tracker's primary source at the mirror while
	// source_id still names the original.
	var canonicalSourceItemID *string
	canonicalSourceURL := ""
	var sourceID int64
	currentSourceURL := ""
	if primaryResult != nil {
		if resolvedSourceItemID := strings.TrimSpace(primaryResult.SourceItemID); resolvedSourceItemID != "" {
			canonicalSourceItemID = &resolvedSourceItemID
		} else {
			canonicalSourceItemID = tracker.SourceItemID
		}
		canonicalSourceURL = strings.TrimSpace(primaryResult.URL)
		if canonicalSourceURL == "" {
			canonicalSourceURL = tracker.SourceURL
		}
		sourceID = tracker.SourceID
		currentSourceURL = tracker.SourceURL
	}

	// The Snapshot fields come straight from the tracker as the cycle read
	// it, not from canonicalSourceURL or currentSourceURL: the first is what
	// this poll wants to write and both are blank when the primary was not
	// asked, while the stale-write guard has to name what the row held,
	// whichever source answered.
	applied, err := p.repo.UpdatePollingState(ctx, repository.PollingUpdate{
		TrackerID:                  tracker.ID,
		SnapshotSourceID:           tracker.SourceID,
		SnapshotSourceURL:          tracker.SourceURL,
		SnapshotLatestKnownChapter: tracker.LatestKnownChapter,
		SourceID:                   sourceID,
		CurrentSourceURL:           currentSourceURL,
		SourceItemID:               canonicalSourceItemID,
		SourceURL:                  canonicalSourceURL,
		LatestKnownChapter:         latest,
		LatestChapterSourceID:      latestChapterSourceID,
		LatestReleaseAt:            latestReleaseAt,
		ClearLatestReleaseAt:       clearLatestReleaseAt,
		PendingLowerChapter:        outcome.pendingLower,
		SourcePolls:                sourcePolls(answered, failed, latest),
		CheckedAt:                  now,
	})
	if err != nil {
		p.logger.Warn("poll update state failed", "trackerId", tracker.ID, "error", err)
	} else if !applied {
		p.logger.Debug("poll update skipped: tracker changed mid-cycle", "trackerId", tracker.ID, "snapshotSourceId", tracker.SourceID)
	}
}

// sourcePolls turns the sites one poll reached into the rotation's memory: a
// site is in step when the number it answered is the chapter the poll stores.
func sourcePolls(answered []pollAnswer, failed []pollCandidate, latest *float64) []repository.SourcePoll {
	polls := make([]repository.SourcePoll, 0, len(answered)+len(failed))
	for _, answer := range answered {
		chapter := answer.result.LatestChapter
		polls = append(polls, repository.SourcePoll{
			SourceID:  answer.candidate.ref.SourceID,
			SourceURL: answer.candidate.ref.SourceURL,
			Primary:   answer.candidate.primary,
			Answered:  true,
			InStep:    chapter != nil && latest != nil && sameChapterNumber(*chapter, *latest),
		})
	}
	for _, candidate := range failed {
		polls = append(polls, repository.SourcePoll{
			SourceID:  candidate.ref.SourceID,
			SourceURL: candidate.ref.SourceURL,
			Primary:   candidate.primary,
		})
	}
	return polls
}

// dropImplausibleChapter returns result without its chapter number when that
// number is too far above the stored one to be real (implausibleChapterAdvance).
// A corrupt number from any single source must not poison the stored
// chapter: once stored, the same source re-confirms it every time it is asked
// and the pending-lower path can never walk it back. It returns a copy, so a
// connector's own result is never rewritten.
func (p *Poller) dropImplausibleChapter(tracker repository.PollingTracker, candidate pollCandidate, result *connectors.MangaResult) *connectors.MangaResult {
	if result.LatestChapter == nil || !implausibleChapterAdvance(tracker.LatestKnownChapter, *result.LatestChapter) {
		return result
	}
	p.logger.Warn("poll source reported an implausible chapter jump; ignoring the number",
		"trackerId", tracker.ID,
		"title", tracker.Title,
		"sourceKey", candidate.ref.SourceKey,
		"stored", derefChapter(tracker.LatestKnownChapter),
		"reported", *result.LatestChapter)
	copied := *result
	copied.LatestChapter = nil
	return &copied
}

// untrustedReleaseLead is how much later than the stored chapter a site not in
// step must date its higher number for that number to stand on its own. The
// same release reaches the aggregators within hours of each other, so a site
// that numbers it differently dates it about the same day; the next chapter
// of anything but a daily series comes days later. A daily series' next
// chapter still gets through as soon as a second site confirms it.
const untrustedReleaseLead = 48 * time.Hour

// releasedWellAfter reports whether reported dates a release at least
// untrustedReleaseLead after the stored chapter's. Without both dates there is
// nothing to compare and the answer is no.
func releasedWellAfter(stored, reported *time.Time) bool {
	return stored != nil && reported != nil && reported.Sub(*stored) >= untrustedReleaseLead
}

// sameAdvance returns the held advance that reports chapter, if any.
func sameAdvance(unconfirmed []pollAnswer, chapter float64) *pollAnswer {
	for index := range unconfirmed {
		if held := unconfirmed[index].result.LatestChapter; held != nil && sameChapterNumber(*held, chapter) {
			return &unconfirmed[index]
		}
	}
	return nil
}

// chapterAdvances reports whether reported would move the tracker forward.
func chapterAdvances(stored *float64, reported float64) bool {
	return stored == nil || (reported > *stored && !sameChapterNumber(reported, *stored))
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
// A primary source is authoritative over its own numbers: whatever it reports is
// stored, corrections downwards included, which is how a source that used to
// over-report gets to fix its own history. An alternate is not — it may be a
// mirror that simply lags — so a lower number from one is only applied once it
// has kept reporting the same value for confirmationDelay. Until then it is
// remembered, not acted on. The same wait applies to the primary when the stored
// number is not its own: an alternate that advanced the tracker on its turn, or
// while the primary was dark, put a number there the primary may simply not
// have indexed yet. With the poll rotating across sites, any site in step that
// agrees with the stored number clears such a pending lower, so it only ever
// confirms when every site asked keeps reporting it for the whole window.
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

	// The immediate correction assumes the stored number is the primary's
	// own. When a fallback supplied it (MangaFire behind its challenge for a
	// few days while ComicK moved the tracker from 120 to 122), the recovered
	// primary still topping out at 121 is exactly the lagging mirror the
	// confirmation exists for; applying it at once walked the tracker back
	// and forth every time a primary came back. No attribution means a number
	// from before the column existed or a hand repair, which the primary has
	// always been trusted to correct.
	storedByPrimary := tracker.LatestChapterSourceID == nil || *tracker.LatestChapterSourceID == tracker.SourceID
	if !usedFallback && storedByPrimary {
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

// betterFallbackResult decides whether candidate should replace current as the
// held answer: between unconfirmed advances from sites not in step, the poll
// keeps the best one, so a mirror that is alive but stale does not shadow a
// fresher one asked after it. Ranking two answers is the shared best-source
// rule; "any answer at all beats none" is this caller's own.
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
