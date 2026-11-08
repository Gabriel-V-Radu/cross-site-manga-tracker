package scheduler

import (
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func TestPollEvery(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ago := func(days int) *time.Time {
		value := now.Add(-time.Duration(days) * 24 * time.Hour)
		return &value
	}
	chapter := 120.0
	idle := 12 * time.Hour

	cases := []struct {
		name    string
		tracker repository.PollingTracker
		want    time.Duration
	}{
		{"reading, active", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(3), SeriesStatus: "ongoing"}, 0},
		{"reading, finished but chapters still coming", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(6), SeriesStatus: "completed"}, 0},
		{"reading, finished and quiet", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(45), SeriesStatus: "completed"}, finishedEvery},
		{"reading, cancelled and quiet", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(31), SeriesStatus: "cancelled"}, finishedEvery},
		{"reading, hiatus and quiet", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(20), SeriesStatus: "hiatus"}, hiatusEvery},
		{"reading, ongoing but dormant", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(120), SeriesStatus: "ongoing"}, dormantReadingEvery},
		{"reading, dormant with no status", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestChapterSeenAt: ago(100)}, dormantReadingEvery},
		{"reading, a late find counts as activity", repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: ago(200), LatestChapterFoundAt: ago(2), SeriesStatus: "completed"}, 0},
		{"reading, no chapter yet", repository.PollingTracker{Status: "reading", SeriesStatus: "completed", LatestReleaseAt: ago(400)}, 0},
		{"plan to read, active", repository.PollingTracker{Status: "plan_to_read", LatestKnownChapter: &chapter, LatestReleaseAt: ago(2)}, planToReadEvery},
		{"plan to read, no chapter yet", repository.PollingTracker{Status: "plan_to_read"}, planToReadEvery},
		{"plan to read, finished and quiet", repository.PollingTracker{Status: "plan_to_read", LatestKnownChapter: &chapter, LatestReleaseAt: ago(60), SeriesStatus: "completed"}, finishedEvery},
		{"plan to read, dormant", repository.PollingTracker{Status: "plan_to_read", LatestKnownChapter: &chapter, LatestReleaseAt: ago(120), SeriesStatus: "ongoing"}, dormantPlanEvery},
		{"on hold, active", repository.PollingTracker{Status: "on_hold", LatestKnownChapter: &chapter, LatestReleaseAt: ago(2)}, idle},
		{"dropped, dormant", repository.PollingTracker{Status: "dropped", LatestKnownChapter: &chapter, LatestReleaseAt: ago(365)}, dormantIdleEvery},
	}
	for _, tc := range cases {
		if got := pollEvery(tc.tracker, idle, now); got != tc.want {
			t.Errorf("%s: pollEvery = %s, want %s", tc.name, got, tc.want)
		}
	}

	// An idle interval configured below the plan-to-read cadence wins.
	plan := repository.PollingTracker{Status: "plan_to_read", LatestKnownChapter: &chapter, LatestReleaseAt: ago(2)}
	if got := pollEvery(plan, time.Hour, now); got != time.Hour {
		t.Errorf("plan to read with a 1h idle interval: pollEvery = %s, want 1h", got)
	}
}

func TestShouldSkipIdleHonoursTheCadence(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	chapter := 50.0
	released := now.Add(-60 * 24 * time.Hour)
	checked := now.Add(-30 * time.Hour)
	poller := &Poller{idleInterval: 12 * time.Hour, now: func() time.Time { return now }}

	finished := repository.PollingTracker{Status: "reading", LatestKnownChapter: &chapter, LatestReleaseAt: &released, SeriesStatus: "completed", LastCheckedAt: &checked}
	if !poller.shouldSkipIdle(finished) {
		t.Fatal("a finished, quiet series checked 30h ago should wait for its 72h turn")
	}
	ongoing := finished
	ongoing.SeriesStatus = "ongoing"
	if poller.shouldSkipIdle(ongoing) {
		t.Fatal("an ongoing reading series quiet for 60 days is still polled every cycle")
	}
}
