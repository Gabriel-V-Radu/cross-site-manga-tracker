package connectors

import (
	"math"
	"strconv"
	"testing"
	"time"
)

// TestValidChapterRejectsNoiseAndNonNumbers pins the guard every connector
// applies before it lets a scraped number become a tracker reading: a corrupt
// upload, a parser that caught the wrong token, or a NaN out of a failed parse
// must never advance a tracker.
func TestValidChapterRejectsNoiseAndNonNumbers(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  bool
	}{
		{name: "ordinary chapter", value: 1044.5, want: true},
		{name: "chapter one", value: 1, want: true},
		{name: "fractional side chapter", value: 0.5, want: true},
		{name: "at the bound", value: MaxPlausibleChapter, want: true},
		{name: "just past the bound", value: MaxPlausibleChapter + 0.5, want: false},
		{name: "far past the bound", value: 123456789, want: false},
		{name: "zero is not a reading", value: 0, want: false},
		{name: "negative", value: -1, want: false},
		{name: "not a number", value: math.NaN(), want: false},
		{name: "positive infinity", value: math.Inf(1), want: false},
		{name: "negative infinity", value: math.Inf(-1), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidChapter(tc.value); got != tc.want {
				t.Fatalf("ValidChapter(%v) = %t, want %t", tc.value, got, tc.want)
			}
		})
	}
}

// TestNovelBoundAcceptsWhatTheComicBoundRejects is the reason the two bounds
// exist separately: a prose serial past 10000 chapters is real, and holding it
// to the comic bound would freeze the tracker on exactly the long catalogue the
// novel sources are there to follow.
func TestNovelBoundAcceptsWhatTheComicBoundRejects(t *testing.T) {
	if MaxPlausibleNovelChapter <= MaxPlausibleChapter {
		t.Fatalf("the novel bound must be looser than the comic one: %d vs %d", MaxPlausibleNovelChapter, MaxPlausibleChapter)
	}

	const longSerial = 12000
	if ValidChapterWithin(longSerial, MaxPlausibleChapter) {
		t.Fatalf("chapter %d must be noise under the comic bound", longSerial)
	}
	if !ValidChapterWithin(longSerial, MaxPlausibleNovelChapter) {
		t.Fatalf("chapter %d must be a real reading under the novel bound", longSerial)
	}

	if !ValidChapterWithin(MaxPlausibleNovelChapter, MaxPlausibleNovelChapter) {
		t.Fatal("the novel bound itself must be valid")
	}
	if ValidChapterWithin(MaxPlausibleNovelChapter+1, MaxPlausibleNovelChapter) {
		t.Fatal("one past the novel bound must be rejected")
	}

	// A caller that forgets to pass its bound rejects everything rather than
	// accepting everything, so a wiring mistake shows up as a stalled tracker
	// and never as noise written to the database.
	if ValidChapterWithin(1, 0) {
		t.Fatal("a zero bound must reject every chapter")
	}
}

// TestSameChapterTolerance covers the epsilon's purpose: two sites' float
// parses of the same number must compare equal, while genuinely different
// chapters must not.
func TestSameChapterTolerance(t *testing.T) {
	// Computed at runtime so the compiler cannot fold the constants and hide
	// the representation error this tolerance exists to absorb.
	tenth, fifth := 0.1, 0.2
	drifted := tenth + fifth

	cases := []struct {
		name string
		a    float64
		b    float64
		want bool
	}{
		{name: "identical", a: 67.5, b: 67.5, want: true},
		{name: "float noise from parsing", a: drifted, b: 0.3, want: true},
		{name: "epsilon is inclusive", a: 0, b: 1e-9, want: true},
		{name: "just outside the epsilon", a: 0, b: 2e-9, want: false},
		{name: "adjacent chapters", a: 1044.5, b: 1044.6, want: false},
		{name: "half chapter apart", a: 67, b: 67.5, want: false},
		{name: "order does not matter", a: 1e-9, b: 0, want: true},
		// NaN and Inf fall out of math.Abs, so a failed parse never reads as a
		// match against anything, itself included.
		{name: "nan never matches", a: math.NaN(), b: math.NaN(), want: false},
		{name: "nan against a number", a: math.NaN(), b: 12, want: false},
		{name: "infinities never match", a: math.Inf(1), b: math.Inf(1), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameChapter(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameChapter(%v, %v) = %t, want %t", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestFormatChapterSpelling pins the exact string, because connectors paste it
// straight into site URLs: an exponent or a trailing zero would build a URL no
// site serves.
func TestFormatChapterSpelling(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{value: 1044.5, want: "1044.5"},
		{value: 304, want: "304"},
		{value: 1, want: "1"},
		{value: 0, want: "0"},
		{value: 0.5, want: "0.5"},
		{value: 2.10, want: "2.1"},
		{value: 99.999, want: "99.999"},
		{value: 12.3456789, want: "12.3456789"},
		// Shortest round-trip spelling, not a fixed number of decimals.
		{value: 1.0 / 3.0, want: "0.3333333333333333"},
		// Both ends of the range that would otherwise pick up an exponent.
		{value: 1e21, want: "1000000000000000000000"},
		{value: 1e-7, want: "0.0000001"},
		{value: MaxPlausibleChapter, want: "10000"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := FormatChapter(tc.value)
			if got != tc.want {
				t.Fatalf("FormatChapter(%v) = %q, want %q", tc.value, got, tc.want)
			}
			// A URL built from this string has to parse back to the same
			// chapter when the connector reads it again.
			back, err := strconv.ParseFloat(got, 64)
			if err != nil {
				t.Fatalf("FormatChapter(%v) = %q, which does not parse back: %v", tc.value, got, err)
			}
			if back != tc.value {
				t.Fatalf("FormatChapter(%v) = %q, which parses back as %v", tc.value, got, back)
			}
		})
	}
}

func TestClampSearchLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{name: "unset", in: 0, want: 10},
		{name: "negative", in: -7, want: 10},
		{name: "one", in: 1, want: 1},
		{name: "under the cap", in: 25, want: 25},
		{name: "at the cap", in: 50, want: 50},
		{name: "over the cap", in: 51, want: 50},
		{name: "absurd", in: 1 << 20, want: 50},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampSearchLimit(tc.in); got != tc.want {
				t.Fatalf("ClampSearchLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseFirstTimeLayoutOrder pins that the layout list is a priority list:
// sites publish ambiguous dates, and a connector orders its layouts to say
// which reading it means.
func TestParseFirstTimeLayoutOrder(t *testing.T) {
	const dayMonth = "2006-02-01"
	const monthDay = "2006-01-02"

	got := ParseFirstTime("2024-03-05", dayMonth, monthDay)
	if got == nil {
		t.Fatal("expected the first layout to accept the value")
	}
	if want := time.Date(2024, time.May, 3, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("first layout should win: got %s, want %s", got, want)
	}

	got = ParseFirstTime("2024-03-05", monthDay, dayMonth)
	if got == nil {
		t.Fatal("expected the first layout to accept the value")
	}
	if want := time.Date(2024, time.March, 5, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("reordering the layouts must change the reading: got %s, want %s", got, want)
	}

	// A layout that does not fit is skipped, not fatal.
	got = ParseFirstTime("2024-03-05", time.RFC1123, time.RFC3339, monthDay)
	if got == nil {
		t.Fatal("a later layout must still be tried")
	}
	if want := time.Date(2024, time.March, 5, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestParseFirstTimeNormalizesToUTC matters because these timestamps are
// compared and stored against readings from other sites, which publish in
// their own offsets.
func TestParseFirstTimeNormalizesToUTC(t *testing.T) {
	got := ParseFirstTime("2024-05-01T12:00:00+02:00", time.RFC3339)
	if got == nil {
		t.Fatal("expected RFC3339 to be accepted")
	}
	if got.Location() != time.UTC {
		t.Fatalf("result location = %s, want UTC", got.Location())
	}
	if want := time.Date(2024, time.May, 1, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestParseFirstTimeRejections(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		layouts []string
	}{
		{name: "blank", value: "", layouts: []string{time.RFC3339}},
		{name: "whitespace only", value: "   \n\t ", layouts: []string{time.RFC3339}},
		{name: "no layouts offered", value: "2024-03-05"},
		{name: "no layout fits", value: "2024-03-05", layouts: []string{time.RFC3339, time.RFC1123}},
		{name: "site published a placeholder", value: "N/A", layouts: []string{"2006-01-02"}},
		{name: "relative date the site renders in JS", value: "2 days ago", layouts: []string{"2006-01-02"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseFirstTime(tc.value, tc.layouts...); got != nil {
				t.Fatalf("ParseFirstTime(%q) = %s, want nil", tc.value, got)
			}
		})
	}
}

// TestParseFirstTimeTrimsSurroundingWhitespace: scraped cells arrive padded by
// the surrounding markup, and every connector would otherwise have to trim.
func TestParseFirstTimeTrimsSurroundingWhitespace(t *testing.T) {
	got := ParseFirstTime("\n  2024-03-05\t ", "2006-01-02")
	if got == nil {
		t.Fatal("padded value must still parse")
	}
	if want := time.Date(2024, time.March, 5, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}
