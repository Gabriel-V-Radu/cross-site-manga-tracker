package connectors

import (
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/sourcepick"
)

// LatestReading folds a site's chapter list into the one reading a connector
// reports: the highest chapter number seen and the release date that goes with
// it. Eight connectors carried their own copy of this loop and three had
// drifted apart on the tie-break — one let the newest upload of an equal
// chapter replace the date, which is how stored dates drift toward "just now"
// on re-uploads. The rule here is sourcepick's, the same one the poller and
// the edit form rank sources with: a number beats none, a higher number wins,
// and between equal numbers a date fills a missing one but never replaces one.
//
// Callers validate the number first (ValidChapter, or the novel bound): the
// accumulator ranks what it is given.
type LatestReading struct {
	best sourcepick.Reading
}

// Add offers one chapter entry. Both values are copied, so the caller's loop
// variables and parsed pointers are never retained.
func (l *LatestReading) Add(chapter float64, releaseAt *time.Time) {
	candidate := sourcepick.Reading{Chapter: &chapter}
	if releaseAt != nil {
		at := releaseAt.UTC()
		candidate.ReleaseAt = &at
	}
	if sourcepick.Better(l.best, candidate) {
		l.best = candidate
	}
}

// Result returns the winning chapter and its date, nil when nothing was added
// (or when the winner carried no date). Fresh copies, safe to store.
func (l *LatestReading) Result() (*float64, *time.Time) {
	if l.best.Chapter == nil {
		return nil, nil
	}
	chapter := *l.best.Chapter
	var releaseAt *time.Time
	if l.best.ReleaseAt != nil {
		at := *l.best.ReleaseAt
		releaseAt = &at
	}
	return &chapter, releaseAt
}

// Chapter is Result's number alone, for callers that only rank.
func (l *LatestReading) Chapter() (float64, bool) {
	if l.best.Chapter == nil {
		return 0, false
	}
	return *l.best.Chapter, true
}
