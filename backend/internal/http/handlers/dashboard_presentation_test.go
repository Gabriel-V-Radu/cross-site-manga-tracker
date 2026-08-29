package handlers

import (
	"testing"
	"time"
)

func TestRelativeTimeSpellsASingleUnitInTheSingular(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{name: "one hour", when: now.Add(-90 * time.Minute), want: "1 hour ago"},
		{name: "several hours", when: now.Add(-5 * time.Hour), want: "5 hours ago"},
		{name: "one day", when: now.Add(-30 * time.Hour), want: "1 day ago"},
		{name: "several days", when: now.Add(-72 * time.Hour), want: "3 days ago"},
		{name: "one month", when: now.Add(-31 * 24 * time.Hour), want: "1 month ago"},
		{name: "several months", when: now.Add(-90 * 24 * time.Hour), want: "3 months ago"},
		{name: "one year", when: now.Add(-400 * 24 * time.Hour), want: "1 year ago"},
		{name: "minutes keep their short form", when: now.Add(-5 * time.Minute), want: "5 min ago"},
		{name: "just now", when: now.Add(-10 * time.Second), want: "just now"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relativeTime(tc.when); got != tc.want {
				t.Fatalf("relativeTime = %q, want %q", got, tc.want)
			}
		})
	}
}
