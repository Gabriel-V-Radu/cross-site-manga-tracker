package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestClassifyOutcome(t *testing.T) {
	chapter := 12.0
	cases := []struct {
		name   string
		result *MangaResult
		err    error
		kind   OutcomeKind
		label  string
	}{
		{name: "answer with a chapter", result: &MangaResult{LatestChapter: &chapter}, kind: OutcomeOK},
		{name: "answer without a chapter", result: &MangaResult{}, kind: OutcomeNoChapter},
		{name: "nil result and nil error", kind: OutcomeNoChapter},
		{name: "caller cancelled", err: fmt.Errorf("request failed: %w", context.Canceled), kind: OutcomeIgnored},
		{name: "breaker refusal", err: fmt.Errorf("x: %w", &SourceCoolingDownError{Host: "a"}), kind: OutcomeCoolingDown, label: "cooling down after repeated failures"},
		{name: "full local queue", err: &SourceCoolingDownError{Host: "a", QueueFull: true}, kind: OutcomeBusy, label: "local request queue full"},
		{name: "series gone", err: &HTTPStatusError{StatusCode: 404}, kind: OutcomeNotFound, label: "series not found (HTTP 404)"},
		{name: "blocked", err: fmt.Errorf("fetch: %w", &HTTPStatusError{StatusCode: 403}), kind: OutcomeFailed, label: "blocked (HTTP 403)"},
		{name: "rate limited", err: &HTTPStatusError{StatusCode: 429}, kind: OutcomeFailed, label: "rate limited (HTTP 429)"},
		{name: "server error", err: &HTTPStatusError{StatusCode: 503}, kind: OutcomeFailed, label: "server error (HTTP 503)"},
		{name: "other status", err: &HTTPStatusError{StatusCode: 410}, kind: OutcomeFailed, label: "unexpected HTTP 410"},
		{
			name:  "challenge named over its 403",
			err:   fmt.Errorf("mangafire is behind a Cloudflare browser challenge and cannot be fetched programmatically: %w", &HTTPStatusError{StatusCode: 403}),
			kind:  OutcomeFailed,
			label: "bot challenge",
		},
		{name: "signer rejected", err: errors.New("mangafire rejected request token (stale signer_bundle.js?)"), kind: OutcomeFailed, label: "request signer rejected"},
		{name: "deadline", err: fmt.Errorf("request failed: %w", context.DeadlineExceeded), kind: OutcomeFailed, label: "timed out"},
		{name: "client timeout", err: &url.Error{Op: "Get", URL: "https://x", Err: timeoutError{}}, kind: OutcomeFailed, label: "timed out"},
		{name: "dns", err: fmt.Errorf("request failed: %w", &net.DNSError{Err: "no such host", Name: "x"}), kind: OutcomeFailed, label: "DNS lookup failed"},
		{name: "refused", err: fmt.Errorf("request failed: %w", &net.OpError{Op: "dial", Err: errors.New("connection refused")}), kind: OutcomeFailed, label: "unreachable"},
		{name: "dial timeout", err: fmt.Errorf("request failed: %w", &net.OpError{Op: "dial", Err: timeoutError{}}), kind: OutcomeFailed, label: "could not connect (timed out)"},
		{name: "connection reset", err: fmt.Errorf("request failed: %w", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}), kind: OutcomeFailed, label: "connection failed"},
		{name: "foreign link", err: errors.New("url does not belong to comick"), kind: OutcomeBadLink, label: "unusable link"},
		{name: "garbled json", err: errors.New("decode response: invalid character"), kind: OutcomeFailed, label: "unreadable response"},
		{name: "layout changed", err: fmt.Errorf("asura series x-1: %w", ErrNoChaptersListed), kind: OutcomeFailed, label: "page lists no chapters (site layout changed?)"},
		{name: "anything else", err: errors.New("no chapter entries found"), kind: OutcomeFailed, label: "request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyOutcome(tc.result, tc.err)
			if got.Kind != tc.kind || got.Label != tc.label {
				t.Fatalf("ClassifyOutcome = %+v, want kind %d label %q", got, tc.kind, tc.label)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// testBoard returns a board on a clock the test moves by hand.
func testBoard() (*SourceHealthBoard, *time.Time) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	board := NewSourceHealthBoard()
	board.clock = func() time.Time { return now }
	return board, &now
}

func runCycle(board *SourceHealthBoard, record func()) {
	board.BeginCycle()
	record()
	board.EndCycle()
}

func boardRow(t *testing.T, board *SourceHealthBoard, key string) SourceHealth {
	t.Helper()
	row, ok := board.view().records[key]
	if !ok {
		t.Fatalf("no record for %q", key)
	}
	return row
}

func TestSourceHealthBoardOneBadCycleIsABlipTwoAreAnOutage(t *testing.T) {
	board, now := testBoard()
	down := fmt.Errorf("request failed: %w", &net.OpError{Op: "dial", Err: errors.New("refused")})

	runCycle(board, func() { board.RecordResolve("asura", RoleTurn, nil, down) })
	row := boardRow(t, board, "asura")
	if row.State != SourceStateDegraded || row.Reason != "unreachable" || row.BadCycles != 1 {
		t.Fatalf("one failed request should only mark the source degraded, got %+v", row)
	}
	firstBad := *now

	*now = now.Add(30 * time.Minute)
	runCycle(board, func() { board.RecordResolve("asura", RoleTurn, nil, down) })
	row = boardRow(t, board, "asura")
	if row.State != SourceStateFailing || row.BadCycles != 2 {
		t.Fatalf("a second cycle with nothing getting through is failing, got %+v", row)
	}
	if !row.Since.Equal(firstBad) {
		t.Fatalf("Since = %v, want the first bad cycle %v", row.Since, firstBad)
	}
	if row.LastError == "" || row.LastFailureAt.IsZero() {
		t.Fatalf("the raw error and its time must be kept: %+v", row)
	}

	*now = now.Add(30 * time.Minute)
	runCycle(board, func() { board.RecordResolve("asura", RoleTurn, &MangaResult{}, nil) })
	row = boardRow(t, board, "asura")
	if row.State != SourceStateOK || row.BadCycles != 0 || !row.Since.IsZero() || row.Reason != "" {
		t.Fatalf("an answered request clears the outage, got %+v", row)
	}
}

// A long cycle with a dark site must not leave the panel blank until it ends:
// a source the running cycle has asked shows that cycle's verdict so far,
// dated from the cycle's start, while the real record only moves at EndCycle.
func TestSourceHealthBoardShowsTheRunningCycleSoFar(t *testing.T) {
	board, now := testBoard()
	chapter := 1.0
	runCycle(board, func() { board.RecordResolve("asura", RoleTurn, &MangaResult{LatestChapter: &chapter}, nil) })

	*now = now.Add(30 * time.Minute)
	board.BeginCycle()
	started := *now
	timeout := fmt.Errorf("request failed: %w", &net.OpError{Op: "dial", Err: timeoutError{}})
	for i := 0; i < 4; i++ {
		*now = now.Add(30 * time.Second)
		board.RecordResolve("asura", RoleTurn, nil, timeout)
	}

	row := boardRow(t, board, "asura")
	if !row.Provisional || row.State != SourceStateFailing || row.Reason != "could not connect (timed out)" || row.Failed != 4 {
		t.Fatalf("the running cycle's verdict so far = %+v", row)
	}
	if !row.Since.Equal(started) {
		t.Fatalf("Since = %v, want the cycle start %v", row.Since, started)
	}
	if board.sources["asura"].state != SourceStateOK {
		t.Fatal("a provisional verdict must not touch the stored record")
	}

	board.EndCycle()
	row = boardRow(t, board, "asura")
	if row.Provisional || row.State != SourceStateFailing || !row.Since.Equal(started) || row.BadCycles != 1 {
		t.Fatalf("after EndCycle = %+v", row)
	}
}

func TestSourceHealthBoardManyFailuresFailAtOnce(t *testing.T) {
	board, _ := testBoard()
	blocked := &HTTPStatusError{StatusCode: 403}
	chapter := 3.0

	runCycle(board, func() {
		for i := 0; i < 9; i++ {
			board.RecordResolve("comick", RoleTurn, nil, blocked)
		}
		board.RecordResolve("comick", RoleTurn, &MangaResult{LatestChapter: &chapter}, nil)
	})
	row := boardRow(t, board, "comick")
	if row.State != SourceStateFailing || row.Reason != "blocked (HTTP 403)" {
		t.Fatalf("9 of 10 series blocked in one cycle is failing, got %+v", row)
	}
	if row.Answered != 1 || row.Failed != 9 {
		t.Fatalf("counts = answered %d failed %d", row.Answered, row.Failed)
	}
}

func TestSourceHealthBoardPartialFailureIsDegraded(t *testing.T) {
	board, _ := testBoard()
	chapter := 3.0

	runCycle(board, func() {
		for i := 0; i < 6; i++ {
			board.RecordResolve("mangahub", RoleTurn, &MangaResult{LatestChapter: &chapter}, nil)
		}
		board.RecordResolve("mangahub", RoleTurn, nil, &HTTPStatusError{StatusCode: 502})
		board.RecordResolve("mangahub", RoleTurn, nil, &HTTPStatusError{StatusCode: 502})
	})
	row := boardRow(t, board, "mangahub")
	if row.State != SourceStateDegraded || row.Reason != "2 of 8 series failed: server error (HTTP 502)" {
		t.Fatalf("a quarter of series failing is degraded, got %+v", row)
	}
}

func TestSourceHealthBoardIgnoresWhatSaysNothingAboutTheSite(t *testing.T) {
	board, _ := testBoard()

	runCycle(board, func() {
		board.RecordResolve("mangadex", RoleTurn, nil, context.Canceled)
		board.RecordResolve("mangadex", RoleTurn, nil, &SourceCoolingDownError{Host: "api.mangadex.org", QueueFull: true})
		board.RecordResolve("mangadex", RoleTurn, nil, errors.New("url does not belong to mangadex"))
	})
	row := boardRow(t, board, "mangadex")
	if row.State != SourceStateUnknown {
		t.Fatalf("busy, cancelled and bad-link outcomes must not produce a verdict, got %+v", row)
	}
	if row.Busy != 1 || row.BadLinks != 1 {
		t.Fatalf("they are still counted for the panel: %+v", row)
	}
}

func TestSourceHealthBoardMostlyNotFoundIsDegraded(t *testing.T) {
	board, _ := testBoard()
	runCycle(board, func() {
		for i := 0; i < 4; i++ {
			board.RecordResolve("webtoons", RoleTurn, nil, &HTTPStatusError{StatusCode: 404})
		}
		board.RecordResolve("webtoons", RoleTurn, &MangaResult{}, nil)
	})
	row := boardRow(t, board, "webtoons")
	if row.State != SourceStateDegraded || row.Reason != "most series not found (links moved?)" {
		t.Fatalf("got %+v", row)
	}
}

func TestSourceHealthBoardUnaskedSourceKeepsItsVerdict(t *testing.T) {
	board, _ := testBoard()
	chapter := 1.0
	runCycle(board, func() { board.RecordResolve("flame", RoleTurn, &MangaResult{LatestChapter: &chapter}, nil) })
	runCycle(board, func() {})
	if row := boardRow(t, board, "flame"); row.State != SourceStateOK {
		t.Fatalf("a cycle that did not need the source is no evidence: %+v", row)
	}
}

func TestSourceHealthBoardAbortDropsTheCycle(t *testing.T) {
	board, _ := testBoard()
	board.BeginCycle()
	board.RecordResolve("asura", RoleTurn, nil, &HTTPStatusError{StatusCode: 500})
	board.AbortCycle()
	board.EndCycle()
	if _, ok := board.view().records["asura"]; ok {
		if row := boardRow(t, board, "asura"); row.State != SourceStateUnknown {
			t.Fatalf("an aborted cycle must not produce a verdict: %+v", row)
		}
	}
}

func TestSourceHealthBoardCoverageAndOrphans(t *testing.T) {
	board, _ := testBoard()
	runCycle(board, func() {
		board.RecordResolve("mangafire", RoleTurn, nil, &HTTPStatusError{StatusCode: 403})
		board.RecordTurnFailure("mangafire", TrackedSeries{ID: 1, Title: "A", ProfileID: 1}, true)
		board.RecordResolve("mangafire", RoleTurn, nil, &HTTPStatusError{StatusCode: 403})
		board.RecordTurnFailure("mangafire", TrackedSeries{ID: 2, Title: "B", ProfileID: 2}, false)
		board.RecordResolve("comick", RoleFallback, &MangaResult{}, nil)
		board.RecordMissingConnector("mangabuddy", TrackedSeries{ID: 3, Title: "C", ProfileID: 1})
	})
	row := boardRow(t, board, "mangafire")
	if row.Covered != 1 || row.UncheckedCount != 1 || len(row.Unchecked) != 1 || row.Unchecked[0].ID != 2 {
		t.Fatalf("coverage = %+v", row)
	}
	if fallback := boardRow(t, board, "comick"); fallback.AsFallback != 1 {
		t.Fatalf("fallback resolves are labelled as such: %+v", fallback)
	}
	orphans := board.view().report.Orphans
	if len(orphans) != 1 || orphans[0].Key != "mangabuddy" || orphans[0].Count != 1 {
		t.Fatalf("orphans = %+v", orphans)
	}
}

func TestSourceHealthBoardLifecycle(t *testing.T) {
	board, now := testBoard()
	if report := board.view().report; report.PollerStarted {
		t.Fatal("a fresh board must not claim the poller runs")
	}
	board.PollerStarted(30 * time.Minute)
	board.BeginCycle()
	if report := board.view().report; !report.CycleRunning || !report.CycleStartedAt.Equal(*now) {
		t.Fatalf("running cycle not reported: %+v", report)
	}
	started := *now
	*now = now.Add(4 * time.Minute)
	board.EndCycle()
	report := board.view().report
	if report.CycleRunning || !report.LastCycleStartedAt.Equal(started) || !report.LastCycleFinishedAt.Equal(*now) || report.Interval != 30*time.Minute {
		t.Fatalf("finished cycle not reported: %+v", report)
	}

	board.BeginCycle()
	board.CycleFailed(errors.New("load trackers for polling: database is locked"))
	if report := board.view().report; report.LastCycleError == "" || report.CycleRunning {
		t.Fatalf("failed cycle not reported: %+v", report)
	}
}

func TestNilSourceHealthBoardIsInert(t *testing.T) {
	var board *SourceHealthBoard
	board.PollerStarted(time.Minute)
	board.BeginCycle()
	board.RecordResolve("a", RoleTurn, nil, errors.New("x"))
	board.RecordTurnFailure("a", TrackedSeries{}, false)
	board.RecordMissingConnector("a", TrackedSeries{})
	board.EndCycle()
	board.AbortCycle()
	board.CycleFailed(nil)
	if view := board.view(); len(view.records) != 0 {
		t.Fatalf("nil board view = %+v", view)
	}
}

type coolingTestConnector struct {
	Site
	left   time.Duration
	reason string
}

func (c *coolingTestConnector) HealthCheck(context.Context) error { return nil }
func (c *coolingTestConnector) ResolveByURL(context.Context, string) (*MangaResult, error) {
	return nil, nil
}
func (c *coolingTestConnector) SearchByTitle(context.Context, string, int) ([]MangaResult, error) {
	return nil, nil
}
func (c *coolingTestConnector) CooldownRemaining() (time.Duration, string) { return c.left, c.reason }

func TestRegistrySourceHealthReport(t *testing.T) {
	registry := NewRegistry()
	for _, connector := range []*coolingTestConnector{
		{Site: Site{SiteKey: "zeta", SiteName: "Zeta"}},
		{Site: Site{SiteKey: "alpha", SiteName: "Alpha"}, left: 10 * time.Minute, reason: "Cloudflare refused us (403 streak)"},
		{Site: Site{SiteKey: "mid", SiteName: "Mid"}},
	} {
		if err := registry.Register(connector); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	board := registry.HealthBoard()
	chapter := 1.0
	runCycle(board, func() { board.RecordResolve("mid", RoleTurn, &MangaResult{LatestChapter: &chapter}, nil) })

	report := registry.SourceHealthReport(time.Now())
	if len(report.Sources) != 3 {
		t.Fatalf("every registered connector gets a row, got %d", len(report.Sources))
	}
	names := []string{report.Sources[0].Name, report.Sources[1].Name, report.Sources[2].Name}
	if names[0] != "Alpha" || names[1] != "Mid" || names[2] != "Zeta" {
		t.Fatalf("rows sorted by name, got %v", names)
	}
	alpha, mid, zeta := report.Sources[0], report.Sources[1], report.Sources[2]
	if alpha.State != SourceStateDegraded || alpha.CooldownLeft != 10*time.Minute || alpha.Reason != "cooling down: Cloudflare refused us (403 streak)" {
		t.Fatalf("a connector cooling down right now is degraded: %+v", alpha)
	}
	if mid.State != SourceStateOK {
		t.Fatalf("mid = %+v", mid)
	}
	if zeta.State != SourceStateUnknown {
		t.Fatalf("a never-asked connector is unknown, not ok: %+v", zeta)
	}
	if report.Count(SourceStateDegraded) != 1 || report.Count(SourceStateFailing) != 0 {
		t.Fatalf("counts wrong: %+v", report)
	}
}
