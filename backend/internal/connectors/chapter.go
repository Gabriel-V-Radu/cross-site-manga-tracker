package connectors

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// MaxPlausibleChapter is the guard against data noise a comic source applies
// to chapter numbers it reports or accepts: a number beyond this is a corrupt
// upload or a parser catching the wrong token, not a release, and must not
// inflate a tracker. (Originally the MangaBuddy/WeebCentral guard, kept by
// MangaHub and MangaUpdates.)
const MaxPlausibleChapter = 10000

// MaxPlausibleNovelChapter is the same guard for prose serials, which run an
// order of magnitude longer than comics: translated web novels routinely pass
// 4000 chapters and the longest serials run past 10000. Holding them to the
// comic bound would not protect anything — it would silently freeze a tracker
// on exactly the long-running catalogue the novel source exists to follow,
// because a rejected number leaves the poller with no reading to store and the
// tracker simply stops advancing.
const MaxPlausibleNovelChapter = 100000

// ValidChapter reports whether f is a comic chapter number worth acting on: a
// real positive number within the plausibility bound. Novel sources want
// ValidChapterWithin with MaxPlausibleNovelChapter.
func ValidChapter(f float64) bool {
	return ValidChapterWithin(f, MaxPlausibleChapter)
}

// ValidChapterWithin is ValidChapter against a caller-chosen upper bound, for
// sources whose medium runs longer than comics do.
func ValidChapterWithin(f float64, max float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f > 0 && f <= max
}

// SameChapter compares two chapter numbers with the tolerance float parsing
// requires ("67.5" from two sites must match).
func SameChapter(a float64, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

// FormatChapter renders a chapter number the way the sites' URLs and labels
// spell it: no exponent, no trailing zeros ("1044.5", "304").
func FormatChapter(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ClampSearchLimit normalizes a caller-supplied search result limit: a
// missing or nonsense limit becomes 10, and no connector fans out beyond 50.
func ClampSearchLimit(n int) int {
	if n <= 0 {
		return 10
	}
	if n > 50 {
		return 50
	}
	return n
}

// ParseFirstTime parses value with the first layout that accepts it and
// returns the result in UTC, or nil when none do (or value is blank).
func ParseFirstTime(value string, layouts ...string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}
