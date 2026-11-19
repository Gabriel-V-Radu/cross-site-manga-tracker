package repository

import (
	"strings"
	"time"
)

func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	if count == 1 {
		return "?"
	}

	var builder strings.Builder
	builder.Grow((count * 2) - 1)
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('?')
	}

	return builder.String()
}

// dbTime is the one form a time is bound to a statement in: UTC, with no
// monotonic clock reading.
//
// The driver stores a time as time.Time.String() (see the note above
// database.Open), and that text is what the columns are ordered and compared
// by. String() spells the zone the value carries, so a time in the server's
// local zone was stored as "... +0100 CET" among rows ending "+0000 UTC" and
// sorted an hour out of place beside them. And it appends the monotonic
// reading of a value that came straight from time.Now() — "... m=+12.3" —
// which made the same instant a different text on every bind: the edit
// form's "did anything change" test (updateTrackerRowTx compares each column
// with IS NOT) then saw a change in a timestamp nobody had touched.
//
// It changes nothing about the spelling of what is already stored: a value
// that was UTC and free of a monotonic reading — every one the poller writes —
// is bound exactly as before.
func dbTime(value time.Time) time.Time {
	return value.UTC().Round(0)
}

// dbTimeArg is dbTime for an optional time: NULL for nil.
func dbTimeArg(value *time.Time) any {
	if value == nil {
		return nil
	}
	return dbTime(*value)
}

// likeEscape is the character likePattern escapes with, named in the
// statement as ESCAPE '\'.
const likeEscape = `\`

// likePattern wraps text for a LIKE ... ESCAPE '\' that finds it anywhere in
// a column, with the characters LIKE gives a meaning to escaped. Without
// that, a search for "100%" matched every title containing "100", and one
// for "_" every title at all.
func likePattern(text string) string {
	replacer := strings.NewReplacer(
		likeEscape, likeEscape+likeEscape,
		"%", likeEscape+"%",
		"_", likeEscape+"_",
	)
	return "%" + replacer.Replace(text) + "%"
}
