package linkscan

import "testing"

func TestScoreCandidateExactMatch(t *testing.T) {
	score := ScoreCandidate("Nano Machine", []string{"Nano Machine"})
	if score != 1.0 {
		t.Fatalf("exact match = %v, want 1.0", score)
	}

	// Normalization: punctuation and case differences are still exact.
	score = ScoreCandidate("mairimashita iruma kun", []string{"Mairimashita! Iruma-kun"})
	if score != 1.0 {
		t.Fatalf("normalized exact match = %v, want 1.0", score)
	}
}

func TestScoreCandidateMatchesAlternateTitle(t *testing.T) {
	score := ScoreCandidate("Kaoru Hana wa Rin to Saku", []string{
		"The Fragrant Flower Blooms With Dignity",
		"Kaoru Hana wa Rin to Saku",
	})
	if score != 1.0 {
		t.Fatalf("alternate-title match = %v, want 1.0", score)
	}
}

// TestScoreCandidatePenalizesDerivativeMarkers pins the failure mode that left
// 91 manual-review rows in the MangaBuddy linking pass: a colored reprint or
// anthology scores high on overlap while being the wrong series to poll.
func TestScoreCandidatePenalizesDerivativeMarkers(t *testing.T) {
	clean := ScoreCandidate("Solo Leveling", []string{"Solo Leveling"})
	colored := ScoreCandidate("Solo Leveling (Colored)", []string{"Solo Leveling"})
	if colored >= clean {
		t.Fatalf("colored reprint (%v) must score below the series itself (%v)", colored, clean)
	}
	if colored >= 0.999 {
		t.Fatalf("colored reprint (%v) must never be bulk-acceptable", colored)
	}

	// A tracker that IS the colored edition keeps its match unpenalized.
	wantColored := ScoreCandidate("Solo Leveling (Colored)", []string{"Solo Leveling (Colored)"})
	if wantColored != 1.0 {
		t.Fatalf("colored-for-colored = %v, want 1.0", wantColored)
	}

	// MangaUpdates catalogs doujinshi as "dj": token-bounded, so it flags
	// "Blue Lock dj - 520" without flagging titles merely containing "dj".
	dj := ScoreCandidate("Blue Lock dj - 520", []string{"Blue Lock"})
	if dj >= 0.999 {
		t.Fatalf("doujinshi (%v) must never be bulk-acceptable", dj)
	}
	adjacent := ScoreCandidate("The Adjacent House", []string{"The Adjacent House"})
	if adjacent != 1.0 {
		t.Fatalf("a title containing the letters dj (%v) must not be penalized", adjacent)
	}
}

func TestScoreCandidatePartialOverlap(t *testing.T) {
	score := ScoreCandidate("Return of the Frozen Player", []string{"Frozen Player"})
	if score <= 0.3 || score >= 1.0 {
		t.Fatalf("partial overlap = %v, want a mid-range score", score)
	}

	unrelated := ScoreCandidate("Machine Uprising", []string{"Nano Machine"})
	if unrelated >= score {
		t.Fatalf("unrelated title (%v) should score below a subtitle match (%v)", unrelated, score)
	}
}

func TestScoreCandidateEmptyInputs(t *testing.T) {
	if got := ScoreCandidate("", []string{"Title"}); got != 0 {
		t.Fatalf("empty candidate = %v, want 0", got)
	}
	if got := ScoreCandidate("Title", nil); got != 0 {
		t.Fatalf("no wanted titles = %v, want 0", got)
	}
}
