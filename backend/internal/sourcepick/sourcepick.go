// Package sourcepick holds the one rule for "which source's reading of a
// series is the better one".
//
// Two callers ask that question: the poller, choosing between the answers a
// tracker's fallback sources gave when the primary went dark, and the tracker
// edit form, choosing which of the linked sources becomes the primary. They
// used to answer it with two hand-written near-copies whose tie-breaks had
// quietly diverged, so the same two mirrors could be ranked one way by a poll
// and the other way by a save.
//
// Deciding which SITE to send a reader to is a different question with a
// different answer (a confirmed chapter link, then the reporting site, then
// the cover); it lives with the card builder and does not belong here.
package sourcepick

import "time"

// Reading is one source's answer about a series: the latest chapter number it
// reports and when it says that chapter landed. Both are optional, and which
// of them is missing is most of what the comparison is about — a site can
// answer with a page that carries no chapter list, or with a chapter number
// and no date at all.
type Reading struct {
	Chapter   *float64
	ReleaseAt *time.Time
}

// Better reports whether candidate should replace current.
//
// A chapter number beats no number, a higher number beats a lower one, and
// between equal numbers a reading that carries a release date beats one that
// does not — it can fill a date the other source never provided.
//
// Between two equal, both-dated readings the incumbent stands. Preferring the
// newer date there looks appealing and is wrong: for one and the same chapter
// number the later timestamp is usually a mirror's re-upload rather than the
// release, and letting it win is exactly how stored dates drift toward "just
// now". Nothing about arriving later makes a reading better.
func Better(current Reading, candidate Reading) bool {
	if candidate.Chapter == nil {
		return false
	}
	if current.Chapter == nil {
		return true
	}
	if *candidate.Chapter != *current.Chapter {
		return *candidate.Chapter > *current.Chapter
	}
	return current.ReleaseAt == nil && candidate.ReleaseAt != nil
}
