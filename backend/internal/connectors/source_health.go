package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// The source health board is what lets the dashboard say "this site is
// failing" without anyone reading the container log. Its evidence is the
// poller's real resolves: every cycle asks each site about the series linked
// to it, which is a far better health signal than a synthetic probe — a site
// can answer its search page and still fail every series it is asked about
// (a changed layout, a signer the site stopped accepting). Requests the
// dashboard makes on the reader's behalf are left out on purpose: a navigation
// cancels them, a page of covers overflows the pacing queue, and a link scan
// guesses URLs that 404, none of which says anything about the site.
//
// The board lives in memory beside the breakers it reports on, so a restart
// resets both together; the poller's first cycle starts with the process, so
// the panel is populated again within one cycle.

// OutcomeKind classifies one resolve for the board.
type OutcomeKind uint8

const (
	// OutcomeIgnored says nothing about the site: the caller gave up.
	OutcomeIgnored OutcomeKind = iota
	// OutcomeOK is an answer with a chapter number.
	OutcomeOK
	// OutcomeNoChapter is an answer without one — a licensed series, an empty
	// English feed. The site is up; the series just has nothing to report.
	OutcomeNoChapter
	// OutcomeNotFound is a 404 for the series asked about: the site is up,
	// the link is stale. Many of them at once means the site moved its URLs.
	OutcomeNotFound
	// OutcomeBadLink is a tracker URL the connector could not even parse.
	OutcomeBadLink
	// OutcomeCoolingDown is a request a breaker refused locally because the
	// site failed repeatedly just before.
	OutcomeCoolingDown
	// OutcomeBusy is a request refused because this process had too many
	// queued for the host; the site did nothing wrong.
	OutcomeBusy
	// OutcomeFailed is the site failing: unreachable, refusing, erroring, or
	// answering with something the connector could not read.
	OutcomeFailed
)

// Outcome is a classified resolve: its kind and, for failures, a short label
// written for the panel ("blocked (HTTP 403)").
type Outcome struct {
	Kind  OutcomeKind
	Label string
}

// ClassifyOutcome sorts one resolve's result into an Outcome. The order of the
// checks matters: a Cloudflare challenge arrives as a 403 and must be named as
// the challenge, and a timeout wrapped in a url.Error must not read as a
// generic network failure.
func ClassifyOutcome(result *MangaResult, err error) Outcome {
	if err == nil {
		if result == nil || result.LatestChapter == nil {
			return Outcome{Kind: OutcomeNoChapter}
		}
		return Outcome{Kind: OutcomeOK}
	}

	if errors.Is(err, context.Canceled) {
		return Outcome{Kind: OutcomeIgnored}
	}

	var cooling *SourceCoolingDownError
	if errors.As(err, &cooling) {
		if cooling.QueueFull {
			return Outcome{Kind: OutcomeBusy, Label: "local request queue full"}
		}
		return Outcome{Kind: OutcomeCoolingDown, Label: "cooling down after repeated failures"}
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "challenge"):
		return Outcome{Kind: OutcomeFailed, Label: "bot challenge"}
	case strings.Contains(message, "rejected request token"):
		return Outcome{Kind: OutcomeFailed, Label: "request signer rejected"}
	}

	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		switch code := statusErr.StatusCode; {
		case code == 404:
			return Outcome{Kind: OutcomeNotFound, Label: "series not found (HTTP 404)"}
		case code == 403:
			return Outcome{Kind: OutcomeFailed, Label: "blocked (HTTP 403)"}
		case code == 429:
			return Outcome{Kind: OutcomeFailed, Label: "rate limited (HTTP 429)"}
		case code >= 500:
			return Outcome{Kind: OutcomeFailed, Label: fmt.Sprintf("server error (HTTP %d)", code)}
		default:
			return Outcome{Kind: OutcomeFailed, Label: fmt.Sprintf("unexpected HTTP %d", code)}
		}
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return Outcome{Kind: OutcomeFailed, Label: "DNS lookup failed"}
	}
	// A connection that never opened is told apart from a slow answer: the
	// first is the network or the site's address (a dead Cloudflare IP on the
	// Pi's route), the second the site itself. Checked before the deadline
	// test because a dial timeout also matches context.DeadlineExceeded.
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		if opErr.Timeout() {
			return Outcome{Kind: OutcomeFailed, Label: "could not connect (timed out)"}
		}
		return Outcome{Kind: OutcomeFailed, Label: "unreachable"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Outcome{Kind: OutcomeFailed, Label: "timed out"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return Outcome{Kind: OutcomeFailed, Label: "timed out"}
	}
	if opErr != nil {
		return Outcome{Kind: OutcomeFailed, Label: "connection failed"}
	}

	// The URL checks every connector opens with (Site.ParseOwnedURL and the
	// per-site shape checks) return plain errors; these phrases are theirs.
	// A link the connector cannot parse is the tracker's problem, not the
	// site's, so it must not count toward the site failing.
	for _, phrase := range []string{"does not belong to", "url is required", "invalid url", "must include", "must look like", "unsupported"} {
		if strings.Contains(message, phrase) {
			return Outcome{Kind: OutcomeBadLink, Label: "unusable link"}
		}
	}

	if errors.Is(err, ErrNoChaptersListed) {
		return Outcome{Kind: OutcomeFailed, Label: "page lists no chapters (site layout changed?)"}
	}
	if strings.Contains(message, "decode response") {
		return Outcome{Kind: OutcomeFailed, Label: "unreadable response"}
	}
	return Outcome{Kind: OutcomeFailed, Label: "request failed"}
}

// ResolveRole says why a source was asked: because it was the tracker's turn
// on this site (the poller rotates each tracker across its linked sites), or
// as a fallback after the site whose turn it was had nothing usable.
type ResolveRole uint8

const (
	RoleTurn ResolveRole = iota
	RoleFallback
)

// TrackedSeries names a tracker on the board, enough to list it in the panel
// and filter it to the profile the reader is looking at.
type TrackedSeries struct {
	ID        int64
	Title     string
	ProfileID int64
}

// SourceState is a source's verdict as of the last cycle that asked it.
type SourceState string

const (
	// SourceStateUnknown: no cycle has asked this source since the process
	// started (polling off, or nothing linked to it is due).
	SourceStateUnknown  SourceState = "unknown"
	SourceStateOK       SourceState = "ok"
	SourceStateDegraded SourceState = "degraded"
	SourceStateFailing  SourceState = "failing"
)

// maxListedSeries caps the per-source lists the panel shows; a site down for
// a whole library would otherwise carry hundreds of titles.
const maxListedSeries = 25

// sourceTally counts one cycle's outcomes for one source.
type sourceTally struct {
	ok, noChapter, notFound, badLinks, failed, refused, busy int
	asFallback                                               int
	// lastFailLabel/lastError describe the most recent failure in the cycle.
	lastFailLabel string
	lastError     string
	// covered and unchecked count trackers whose turn this source had and
	// failed: covered when another linked site answered instead, unchecked
	// when nothing did.
	covered        int
	uncheckedCount int
	unchecked      []TrackedSeries
	// orphaned are trackers whose primary source has no connector at all.
	orphanedCount int
	orphaned      []TrackedSeries
}

func (t *sourceTally) asked() int    { return t.ok + t.noChapter + t.notFound + t.failed }
func (t *sourceTally) answered() int { return t.ok + t.noChapter + t.notFound }

// sourceRecord is everything the board remembers about one source.
type sourceRecord struct {
	last          sourceTally
	evaluated     bool
	state         SourceState
	reason        string
	badStreak     int
	since         time.Time
	lastAskedAt   time.Time
	lastSuccessAt time.Time
	lastFailureAt time.Time
	lastError     string
}

// SourceHealthBoard collects the poller's outcomes per source and folds each
// cycle into a verdict. Shard goroutines record concurrently; only EndCycle
// folds, after the cycle's goroutines are done. A nil board ignores every
// call, so code paths that run without one need no guards.
type SourceHealthBoard struct {
	mu    sync.Mutex
	clock func() time.Time

	pollerStarted bool
	interval      time.Duration

	cycleRunning        bool
	cycleStartedAt      time.Time
	lastCycleStartedAt  time.Time
	lastCycleFinishedAt time.Time
	lastCycleError      string

	current map[string]*sourceTally
	sources map[string]*sourceRecord
}

func NewSourceHealthBoard() *SourceHealthBoard {
	return &SourceHealthBoard{
		clock:   time.Now,
		current: map[string]*sourceTally{},
		sources: map[string]*sourceRecord{},
	}
}

func (b *SourceHealthBoard) now() time.Time { return b.clock().UTC() }

// PollerStarted records that this process polls, and how often. Without it
// the panel must say polling is off rather than "all good".
func (b *SourceHealthBoard) PollerStarted(interval time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pollerStarted = true
	b.interval = interval
}

// BeginCycle opens a cycle. Outcomes recorded before it (none, normally) are
// kept and folded with it.
func (b *SourceHealthBoard) BeginCycle() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cycleRunning = true
	b.cycleStartedAt = b.now()
}

// CycleFailed closes a cycle that could not even load its trackers.
func (b *SourceHealthBoard) CycleFailed(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cycleRunning = false
	b.lastCycleStartedAt = b.cycleStartedAt
	b.lastCycleFinishedAt = b.now()
	if err != nil {
		b.lastCycleError = err.Error()
	}
	b.current = map[string]*sourceTally{}
}

// AbortCycle drops a cycle cut short by shutdown: its cancelled resolves
// would otherwise read as every site failing at once.
func (b *SourceHealthBoard) AbortCycle() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cycleRunning = false
	b.current = map[string]*sourceTally{}
}

// RecordResolve files one resolve against sourceKey.
func (b *SourceHealthBoard) RecordResolve(sourceKey string, role ResolveRole, result *MangaResult, err error) {
	if b == nil || sourceKey == "" {
		return
	}
	outcome := ClassifyOutcome(result, err)
	if outcome.Kind == OutcomeIgnored {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	tally := b.tallyLocked(sourceKey)
	record := b.recordLocked(sourceKey)
	now := b.now()
	if role == RoleFallback {
		tally.asFallback++
	}

	switch outcome.Kind {
	case OutcomeOK:
		tally.ok++
	case OutcomeNoChapter:
		tally.noChapter++
	case OutcomeNotFound:
		tally.notFound++
	case OutcomeBadLink:
		tally.badLinks++
	case OutcomeBusy:
		tally.busy++
	case OutcomeCoolingDown:
		tally.refused++
	case OutcomeFailed:
		tally.failed++
	}

	switch outcome.Kind {
	case OutcomeOK, OutcomeNoChapter, OutcomeNotFound:
		record.lastAskedAt = now
		record.lastSuccessAt = now
	case OutcomeFailed:
		record.lastAskedAt = now
		record.lastFailureAt = now
		record.lastError = truncateError(err)
		tally.lastFailLabel = outcome.Label
		tally.lastError = record.lastError
	}
}

// RecordTurnFailure files a tracker whose turn this cycle went to sourceKey,
// which failed it; covered says whether another linked site answered instead.
func (b *SourceHealthBoard) RecordTurnFailure(sourceKey string, series TrackedSeries, covered bool) {
	if b == nil || sourceKey == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tally := b.tallyLocked(sourceKey)
	if covered {
		tally.covered++
		return
	}
	tally.uncheckedCount++
	if len(tally.unchecked) < maxListedSeries {
		tally.unchecked = append(tally.unchecked, series)
	}
}

// RecordMissingConnector files a tracker whose primary source has no
// connector, which the poller can only skip.
func (b *SourceHealthBoard) RecordMissingConnector(sourceKey string, series TrackedSeries) {
	if b == nil || sourceKey == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tally := b.tallyLocked(sourceKey)
	tally.orphanedCount++
	if len(tally.orphaned) < maxListedSeries {
		tally.orphaned = append(tally.orphaned, series)
	}
}

func (b *SourceHealthBoard) tallyLocked(key string) *sourceTally {
	tally, ok := b.current[key]
	if !ok {
		tally = &sourceTally{}
		b.current[key] = tally
	}
	return tally
}

func (b *SourceHealthBoard) recordLocked(key string) *sourceRecord {
	record, ok := b.sources[key]
	if !ok {
		record = &sourceRecord{state: SourceStateUnknown}
		b.sources[key] = record
	}
	return record
}

// EndCycle folds the cycle's outcomes into each source's verdict. A source
// the cycle never asked keeps the verdict it had: not being needed (only idle
// trackers point at it) is not evidence either way.
func (b *SourceHealthBoard) EndCycle() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.cycleRunning = false
	b.lastCycleStartedAt = b.cycleStartedAt
	b.lastCycleFinishedAt = now
	b.lastCycleError = ""
	// A run of bad cycles is dated from the start of its first cycle, when
	// the failing began, not from when that cycle finished.
	startedAt := b.cycleStartedAt
	if startedAt.IsZero() {
		startedAt = now
	}

	for key, tally := range b.current {
		record := b.recordLocked(key)
		record.last = *tally
		if tally.asked()+tally.refused == 0 {
			// Only bad links, busy refusals or orphans: nothing that
			// tested the site.
			continue
		}
		record.evaluated = true
		foldVerdict(record, tally, startedAt)
	}
	b.current = map[string]*sourceTally{}
}

// foldVerdict turns one cycle's tally into the source's state.
//
// A "bad" cycle is one where nothing got through — every request failed or
// was refused by a cooldown — or where nearly everything failed. One bad
// cycle with few requests is a blip (a timeout, a hiccup) and only marks the
// source degraded; it fails once that repeats in the next cycle, or when a
// single cycle saw it fail several series. A source failing a share of its
// series while answering the rest is degraded, as is one whose series mostly
// 404: the site is up, but its links have moved.
func foldVerdict(record *sourceRecord, tally *sourceTally, now time.Time) {
	asked := tally.asked()
	answered := tally.answered()
	failRate := 0.0
	if asked > 0 {
		failRate = float64(tally.failed) / float64(asked)
	}

	bad := answered == 0 || (tally.failed >= 3 && failRate >= 0.8)
	if bad {
		if record.badStreak == 0 {
			record.since = now
		}
		record.badStreak++
	} else {
		record.badStreak = 0
		record.since = time.Time{}
	}

	failureReason := tally.lastFailLabel
	if failureReason == "" && tally.refused > 0 {
		failureReason = "cooling down after repeated failures"
	}

	switch {
	case bad && (record.badStreak >= 2 || tally.failed >= 3):
		record.state = SourceStateFailing
		record.reason = failureReason
	case bad:
		record.state = SourceStateDegraded
		record.reason = failureReason
	case tally.failed >= 2 && failRate >= 0.2:
		record.state = SourceStateDegraded
		record.reason = fmt.Sprintf("%d of %d series failed: %s", tally.failed, asked, failureReason)
	case tally.notFound >= 3 && tally.notFound*2 >= asked:
		record.state = SourceStateDegraded
		record.reason = "most series not found (links moved?)"
	default:
		record.state = SourceStateOK
		record.reason = ""
	}
}

// truncateError keeps the panel's raw error line readable; connector errors
// can carry whole URLs and wrapped chains.
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	const limit = 240
	if len([]rune(text)) > limit {
		text = string([]rune(text)[:limit]) + "…"
	}
	return text
}

// SourceHealth is one source's row in a report.
type SourceHealth struct {
	Key   string
	Name  string
	State SourceState
	// Reason is the short label behind a degraded or failing state.
	Reason string
	// LastError is the most recent raw failure text, for whoever goes to fix it.
	LastError     string
	LastAskedAt   time.Time
	LastSuccessAt time.Time
	LastFailureAt time.Time
	// Since is when the current run of bad cycles began; zero when healthy.
	Since time.Time
	// BadCycles is how many consecutive cycles nothing got through.
	BadCycles int
	// Provisional marks a verdict and counts taken from the cycle still
	// running rather than the last finished one.
	Provisional bool

	// Counts from the last cycle that asked this source, or from the running
	// one when Provisional.
	Answered   int
	NoChapter  int
	NotFound   int
	Failed     int
	Refused    int
	Busy       int
	BadLinks   int
	AsFallback int

	// Trackers whose turn this source had and failed last cycle.
	Covered        int
	UncheckedCount int
	Unchecked      []TrackedSeries

	// A breaker open right now, from the connector's own breaker or the
	// shared per-host throttle.
	CooldownLeft   time.Duration
	CooldownReason string
}

// OrphanedSource is a source key trackers point at with no connector behind it.
type OrphanedSource struct {
	Key      string
	Count    int
	Trackers []TrackedSeries
}

// SourceHealthReport is a point-in-time copy of the board plus the breakers'
// live state, safe to render without holding any lock.
type SourceHealthReport struct {
	PollerStarted       bool
	Interval            time.Duration
	CycleRunning        bool
	CycleStartedAt      time.Time
	LastCycleStartedAt  time.Time
	LastCycleFinishedAt time.Time
	LastCycleError      string
	Sources             []SourceHealth
	Orphans             []OrphanedSource
}

// Count returns how many sources are in state.
func (r SourceHealthReport) Count(state SourceState) int {
	count := 0
	for _, source := range r.Sources {
		if source.State == state {
			count++
		}
	}
	return count
}

// boardView is the board's half of a report, before the registry adds live
// cooldowns and display names.
type boardView struct {
	report  SourceHealthReport
	records map[string]SourceHealth
}

func (b *SourceHealthBoard) view() boardView {
	view := boardView{records: map[string]SourceHealth{}}
	if b == nil {
		return view
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	view.report = SourceHealthReport{
		PollerStarted:       b.pollerStarted,
		Interval:            b.interval,
		CycleRunning:        b.cycleRunning,
		CycleStartedAt:      b.cycleStartedAt,
		LastCycleStartedAt:  b.lastCycleStartedAt,
		LastCycleFinishedAt: b.lastCycleFinishedAt,
		LastCycleError:      b.lastCycleError,
	}
	for key, stored := range b.sources {
		record := *stored
		provisional := false
		// While a cycle runs, a source it has already asked is shown with the
		// verdict that cycle points to so far, folded on a copy so the real
		// record only moves at EndCycle. Without this the panel said nothing
		// for as long as the cycle lasted — and a site going dark is exactly
		// what makes a cycle long, every tracker on it waiting out a timeout.
		if tally, ok := b.current[key]; ok && b.cycleRunning && tally.asked()+tally.refused > 0 {
			record.last = *tally
			foldVerdict(&record, tally, b.cycleStartedAt)
			record.evaluated = true
			provisional = true
		}
		row := SourceHealth{
			Key:            key,
			State:          record.state,
			Reason:         record.reason,
			LastError:      record.lastError,
			LastAskedAt:    record.lastAskedAt,
			LastSuccessAt:  record.lastSuccessAt,
			LastFailureAt:  record.lastFailureAt,
			Since:          record.since,
			BadCycles:      record.badStreak,
			Provisional:    provisional,
			Answered:       record.last.answered(),
			NoChapter:      record.last.noChapter,
			NotFound:       record.last.notFound,
			Failed:         record.last.failed,
			Refused:        record.last.refused,
			Busy:           record.last.busy,
			BadLinks:       record.last.badLinks,
			AsFallback:     record.last.asFallback,
			Covered:        record.last.covered,
			UncheckedCount: record.last.uncheckedCount,
			Unchecked:      append([]TrackedSeries(nil), record.last.unchecked...),
		}
		if !record.evaluated {
			row.State = SourceStateUnknown
		}
		view.records[key] = row
		if record.last.orphanedCount > 0 {
			view.report.Orphans = append(view.report.Orphans, OrphanedSource{
				Key:      key,
				Count:    record.last.orphanedCount,
				Trackers: append([]TrackedSeries(nil), record.last.orphaned...),
			})
		}
	}
	sort.Slice(view.report.Orphans, func(i, j int) bool { return view.report.Orphans[i].Key < view.report.Orphans[j].Key })
	return view
}

// SourceHealthReport merges the board's verdicts with the breakers' live
// state into one row per registered connector, sorted by name. A connector
// that is cooling down right now is at least degraded, whatever its last
// cycle said: the next requests to it will be refused.
func (r *Registry) SourceHealthReport(now time.Time) SourceHealthReport {
	view := r.board.view()
	report := view.report

	circuits := OpenHostCircuits(now)

	r.mu.RLock()
	list := make([]Connector, 0, len(r.connectors))
	for _, connector := range r.connectors {
		list = append(list, connector)
	}
	hostOwner := map[string]string{}
	for _, circuit := range circuits {
		if connector, ok := r.findByHostLocked(strings.ToLower(circuit.Host)); ok {
			hostOwner[circuit.Host] = connector.Key()
		}
	}
	r.mu.RUnlock()

	for _, connector := range list {
		row, ok := view.records[connector.Key()]
		if !ok {
			row = SourceHealth{Key: connector.Key(), State: SourceStateUnknown}
		}
		row.Name = connector.Name()

		if reporter, isReporter := connector.(CooldownReporter); isReporter {
			if remaining, reason := reporter.CooldownRemaining(); remaining > 0 {
				row.CooldownLeft = remaining
				row.CooldownReason = reason
			}
		}
		for _, circuit := range circuits {
			if hostOwner[circuit.Host] != connector.Key() || circuit.BlockedFor <= row.CooldownLeft {
				continue
			}
			row.CooldownLeft = circuit.BlockedFor
			row.CooldownReason = circuit.Host + " failed repeatedly"
		}
		if row.CooldownLeft > 0 && (row.State == SourceStateOK || row.State == SourceStateUnknown) {
			row.State = SourceStateDegraded
			if row.Reason == "" {
				row.Reason = "cooling down: " + row.CooldownReason
			}
		}
		report.Sources = append(report.Sources, row)
	}

	sort.Slice(report.Sources, func(i, j int) bool {
		return strings.ToLower(report.Sources[i].Name) < strings.ToLower(report.Sources[j].Name)
	})
	return report
}
