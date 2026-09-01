package connectors

import (
	"testing"
	"time"
)

func TestLatestReadingEmpty(t *testing.T) {
	var reading LatestReading
	chapter, at := reading.Result()
	if chapter != nil || at != nil {
		t.Fatalf("empty accumulator returned %v %v", chapter, at)
	}
	if _, ok := reading.Chapter(); ok {
		t.Fatal("empty accumulator reports a chapter")
	}
}

func TestLatestReadingKeepsTheHighestChapterAndItsDate(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

	var reading LatestReading
	reading.Add(240, &newer)
	reading.Add(241, &older) // a higher number with an older date still wins
	reading.Add(239, nil)

	chapter, at := reading.Result()
	if chapter == nil || *chapter != 241 {
		t.Fatalf("chapter = %v, want 241", chapter)
	}
	if at == nil || !at.Equal(older) {
		t.Fatalf("date = %v, want the winner's own date %v", at, older)
	}
}

// The tie-break is sourcepick's: on the same chapter a date fills a missing
// one and never replaces one, because for one chapter number the later upload
// is a mirror's re-upload, not the release.
func TestLatestReadingTieBreakFillsButNeverReplacesADate(t *testing.T) {
	first := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	reupload := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

	var reading LatestReading
	reading.Add(241, nil)
	reading.Add(241, &first)
	_, at := reading.Result()
	if at == nil || !at.Equal(first) {
		t.Fatalf("a date must fill a missing one, got %v", at)
	}

	reading.Add(241, &reupload)
	_, at = reading.Result()
	if at == nil || !at.Equal(first) {
		t.Fatalf("a later date must not replace the first, got %v", at)
	}
}

// Result hands out copies: a caller that stores them must not be able to
// change what the accumulator holds, and Add must not retain the caller's
// pointer.
func TestLatestReadingCopiesValues(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var reading LatestReading
	reading.Add(10, &at)
	at = at.AddDate(1, 0, 0)

	chapter, got := reading.Result()
	if got.Year() != 2026 {
		t.Fatalf("Add retained the caller's pointer: %v", got)
	}
	*chapter = 99
	if again, _ := reading.Result(); *again != 10 {
		t.Fatalf("Result aliased the accumulator: %v", *again)
	}
}
