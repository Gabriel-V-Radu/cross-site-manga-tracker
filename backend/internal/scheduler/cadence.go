package scheduler

import (
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// How often a series that has stopped moving is still asked about. A finished
// series gets the odd extra or side story, a hiatus ends, a "dead" series comes
// back — so none of them is ever dropped, only asked less often. Each rule
// needs the series to have gone quiet as well, because the publication status
// sites report is the original run's: a manhwa finished in Korea can have its
// English chapters still coming out every week, and those must keep being
// caught on the first cycle. A new chapter makes the series active again, and
// it goes back to the normal cadence on its own.
const (
	finishedQuietAfter = 30 * 24 * time.Hour
	finishedEvery      = 72 * time.Hour

	hiatusQuietAfter = 14 * 24 * time.Hour
	hiatusEvery      = 24 * time.Hour

	dormantAfter        = 90 * 24 * time.Hour
	dormantReadingEvery = 24 * time.Hour
	dormantIdleEvery    = 72 * time.Hour
)

// pollEvery is the least time between two polls of the tracker: zero for a
// reading tracker that is still moving (every cycle), idleInterval for the
// other statuses, and longer for a series that is finished, on hiatus or has
// been quiet for months (see the constants above).
func pollEvery(tracker repository.PollingTracker, idleInterval time.Duration, now time.Time) time.Duration {
	reading := strings.EqualFold(strings.TrimSpace(tracker.Status), "reading")
	interval := time.Duration(0)
	if !reading {
		interval = idleInterval
	}

	quiet, known := quietFor(tracker, now)
	if !known {
		return interval
	}
	switch tracker.SeriesStatus {
	case connectors.SeriesStatusCompleted, connectors.SeriesStatusCancelled:
		if quiet >= finishedQuietAfter {
			interval = max(interval, finishedEvery)
		}
	case connectors.SeriesStatusHiatus:
		if quiet >= hiatusQuietAfter {
			interval = max(interval, hiatusEvery)
		}
	}
	if quiet >= dormantAfter {
		if reading {
			interval = max(interval, dormantReadingEvery)
		} else {
			interval = max(interval, dormantIdleEvery)
		}
	}
	return interval
}

// quietFor is how long ago the tracker's latest chapter came out, as best the
// stored dates tell: the release date, or when a poll found the chapter if
// that is later; failing both, when the number was first recorded (sources
// that give no dates). Unknown when the tracker has no chapter at all.
func quietFor(tracker repository.PollingTracker, now time.Time) (time.Duration, bool) {
	if tracker.LatestKnownChapter == nil {
		return 0, false
	}
	var last *time.Time
	for _, candidate := range []*time.Time{tracker.LatestReleaseAt, tracker.LatestChapterFoundAt} {
		if candidate != nil && (last == nil || candidate.After(*last)) {
			last = candidate
		}
	}
	if last == nil {
		last = tracker.LatestChapterSeenAt
	}
	if last == nil {
		return 0, false
	}
	return now.Sub(*last), true
}
