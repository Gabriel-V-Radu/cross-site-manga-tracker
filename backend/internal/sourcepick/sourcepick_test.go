package sourcepick

import (
	"testing"
	"time"
)

func chapter(value float64) *float64 { return &value }

func TestBetter(t *testing.T) {
	older := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		current   Reading
		candidate Reading
		want      bool
	}{
		{
			name:      "a chapter number beats nothing on record",
			current:   Reading{},
			candidate: Reading{Chapter: chapter(10)},
			want:      true,
		},
		{
			name:      "an answer without a chapter number never wins",
			current:   Reading{Chapter: chapter(10)},
			candidate: Reading{},
			want:      false,
		},
		{
			name:      "two chapterless answers leave the incumbent alone",
			current:   Reading{ReleaseAt: &older},
			candidate: Reading{ReleaseAt: &newer},
			want:      false,
		},
		{
			name:      "a higher chapter wins",
			current:   Reading{Chapter: chapter(10)},
			candidate: Reading{Chapter: chapter(12)},
			want:      true,
		},
		{
			name:      "a lower chapter loses",
			current:   Reading{Chapter: chapter(12)},
			candidate: Reading{Chapter: chapter(10)},
			want:      false,
		},
		{
			name:      "a higher chapter wins even without a date",
			current:   Reading{Chapter: chapter(10), ReleaseAt: &newer},
			candidate: Reading{Chapter: chapter(12)},
			want:      true,
		},
		{
			name:      "on equal chapters a release date fills a missing one",
			current:   Reading{Chapter: chapter(10)},
			candidate: Reading{Chapter: chapter(10), ReleaseAt: &older},
			want:      true,
		},
		{
			name:      "on equal chapters a missing date does not replace a date",
			current:   Reading{Chapter: chapter(10), ReleaseAt: &older},
			candidate: Reading{Chapter: chapter(10)},
			want:      false,
		},
		{
			// The reconciled tie-break: the edit form used to promote the
			// newer-dated source here, the poller kept the incumbent. Keeping
			// the incumbent is now the single answer.
			name:      "on equal chapters a newer date does not unseat an older one",
			current:   Reading{Chapter: chapter(10), ReleaseAt: &older},
			candidate: Reading{Chapter: chapter(10), ReleaseAt: &newer},
			want:      false,
		},
		{
			name:      "on equal chapters an older date does not unseat a newer one",
			current:   Reading{Chapter: chapter(10), ReleaseAt: &newer},
			candidate: Reading{Chapter: chapter(10), ReleaseAt: &older},
			want:      false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Better(testCase.current, testCase.candidate); got != testCase.want {
				t.Fatalf("Better() = %v, want %v", got, testCase.want)
			}
		})
	}
}
