package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func TestBuildSourceHealthIndicator(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	finished := connectors.SourceHealthReport{
		PollerStarted:       true,
		Interval:            30 * time.Minute,
		LastCycleStartedAt:  now.Add(-20 * time.Minute),
		LastCycleFinishedAt: now.Add(-10 * time.Minute),
	}
	withSources := func(report connectors.SourceHealthReport, states ...connectors.SourceState) connectors.SourceHealthReport {
		for _, state := range states {
			report.Sources = append(report.Sources, connectors.SourceHealth{Key: string(state), State: state})
		}
		return report
	}

	cases := []struct {
		name   string
		report connectors.SourceHealthReport
		tone   string
		label  string
	}{
		// The local instance runs with polling off; it must never claim the
		// sites are fine just because nothing has failed.
		{name: "polling off", report: withSources(connectors.SourceHealthReport{}, connectors.SourceStateUnknown), tone: "unknown", label: "Polling off"},
		{name: "first cycle running", report: connectors.SourceHealthReport{PollerStarted: true, CycleRunning: true}, tone: "unknown", label: "Checking sources"},
		{name: "all fine", report: withSources(finished, connectors.SourceStateOK, connectors.SourceStateUnknown), tone: "ok", label: "Sources OK"},
		{name: "one failing", report: withSources(finished, connectors.SourceStateOK, connectors.SourceStateFailing, connectors.SourceStateDegraded), tone: "failing", label: "1 source failing"},
		{name: "two degraded", report: withSources(finished, connectors.SourceStateDegraded, connectors.SourceStateDegraded), tone: "degraded", label: "2 sources with problems"},
		{name: "cycle error", report: func() connectors.SourceHealthReport { r := finished; r.LastCycleError = "db locked"; return r }(), tone: "degraded", label: "Poll cycle failed"},
		{
			name: "stalled poller",
			report: func() connectors.SourceHealthReport {
				r := finished
				r.LastCycleFinishedAt = now.Add(-5 * time.Hour)
				return r
			}(),
			tone:  "degraded",
			label: "Poller stalled",
		},
		{
			name: "orphaned trackers",
			report: func() connectors.SourceHealthReport {
				r := finished
				r.Orphans = []connectors.OrphanedSource{{Key: "mangabuddy", Count: 3}}
				return r
			}(),
			tone:  "degraded",
			label: "3 trackers not polled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSourceHealthIndicator(tc.report, now)
			if got.Tone != tc.tone || got.Label != tc.label {
				t.Fatalf("indicator = %+v, want tone %q label %q", got, tc.tone, tc.label)
			}
		})
	}
}

func TestBuildSourceHealthModalOrdersWorstFirstAndFiltersTitlesByProfile(t *testing.T) {
	now := time.Now().UTC()
	report := connectors.SourceHealthReport{
		PollerStarted:       true,
		Interval:            30 * time.Minute,
		LastCycleStartedAt:  now.Add(-15 * time.Minute),
		LastCycleFinishedAt: now.Add(-5 * time.Minute),
		Sources: []connectors.SourceHealth{
			{Name: "Asura", State: connectors.SourceStateOK, Answered: 12, LastSuccessAt: now.Add(-6 * time.Minute), LastError: "old blip"},
			{
				Name:           "MangaFire",
				State:          connectors.SourceStateFailing,
				Reason:         "bot challenge",
				LastError:      "mangafire is behind a Cloudflare browser challenge",
				Since:          now.Add(-2 * time.Hour),
				BadCycles:      4,
				Failed:         1,
				Refused:        6,
				Covered:        5,
				UncheckedCount: 2,
				Unchecked: []connectors.TrackedSeries{
					{ID: 1, Title: "Mine", ProfileID: 1},
					{ID: 2, Title: "Theirs", ProfileID: 2},
				},
				CooldownLeft:   25 * time.Minute,
				CooldownReason: "Cloudflare browser challenge",
			},
			{Name: "Webtoons", State: connectors.SourceStateUnknown},
		},
	}

	data := buildSourceHealthModal(report, 1, now)
	if len(data.Rows) != 3 || data.Rows[0].Name != "MangaFire" || data.Rows[1].Name != "Asura" || data.Rows[2].Name != "Webtoons" {
		t.Fatalf("rows not worst-first: %+v", data.Rows)
	}
	failing := data.Rows[0]
	if failing.StateLabel != "Failing" || failing.Reason != "bot challenge" || failing.LastError == "" {
		t.Fatalf("failing row = %+v", failing)
	}
	if len(failing.Unchecked) != 1 || failing.Unchecked[0] != "Mine" || failing.UncheckedMore != 1 {
		t.Fatalf("unchecked titles must be the active profile's, the rest counted: %+v", failing)
	}
	if !strings.Contains(failing.Coverage, "5 trackers fell back") || !strings.Contains(failing.Counts, "6 skipped while cooling down") {
		t.Fatalf("coverage/counts = %q / %q", failing.Coverage, failing.Counts)
	}
	facts := strings.Join(failing.Facts, " | ")
	if !strings.Contains(facts, "4 cycles in a row") || !strings.Contains(facts, "Cooling down for 25 min more") {
		t.Fatalf("facts = %q", facts)
	}
	if data.Rows[1].LastError != "" {
		t.Fatal("an old error on a healthy source is not shown")
	}
	if data.Rows[2].StateLabel != "No data" {
		t.Fatalf("unknown row = %+v", data.Rows[2])
	}
	if !strings.HasPrefix(data.CycleLine, "Last poll cycle finished 5 min ago (took 10 min).") {
		t.Fatalf("cycle line = %q", data.CycleLine)
	}
}

func TestBuildSourceHealthRowSaysWhichCycleItDescribes(t *testing.T) {
	now := time.Now().UTC()
	running := buildSourceHealthRow(connectors.SourceHealth{
		Name: "AsuraComic", State: connectors.SourceStateFailing, Provisional: true, Failed: 4,
		LastAskedAt: now, UncheckedCount: 1, Unchecked: []connectors.TrackedSeries{{Title: "Mine", ProfileID: 1}},
	}, 1)
	if running.Counts != "This cycle so far: 4 failed" || running.CycleWord != "this cycle so far" {
		t.Fatalf("provisional row = %+v", running)
	}

	// Asked during the running cycle but with no verdict yet: it was asked, so
	// it must not also claim the poller never asked it.
	asked := buildSourceHealthRow(connectors.SourceHealth{Name: "MangaFire", State: connectors.SourceStateUnknown, LastAskedAt: now, LastSuccessAt: now}, 1)
	for _, fact := range asked.Facts {
		if strings.Contains(fact, "Not asked") {
			t.Fatalf("a source with an answer cannot be 'not asked': %v", asked.Facts)
		}
	}
}

func TestBuildSourceHealthModalPollingOff(t *testing.T) {
	data := buildSourceHealthModal(connectors.SourceHealthReport{}, 1, time.Now())
	if data.Notice == "" || data.CycleLine != "" {
		t.Fatalf("polling off must be said, not implied: %+v", data)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                               "1s",
		45 * time.Second:                "45s",
		12*time.Minute + 20*time.Second: "12 min",
		3 * time.Hour:                   "3 h",
		3*time.Hour + 5*time.Minute:     "3 h 5 min",
		59*time.Minute + 50*time.Second: "1 h",
	}
	for span, want := range cases {
		if got := shortDuration(span); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", span, got, want)
		}
	}
}
