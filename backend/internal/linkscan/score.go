package linkscan

import (
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// suspectMarkers are words that mark a candidate as a derivative work rather
// than the series itself. They are what made the MangaBuddy linking pass leave
// 91 rows for manual review: a "(Colored)" reprint or an anthology scores high
// on title similarity while being the wrong thing to poll.
var suspectMarkers = []string{
	"colored", "coloured", "full color", "anthology", "doujinshi", "dj",
	"oneshot", "one shot", "novel", "fan colored", "official colored",
}

// ScoreCandidate rates how well a candidate title matches a tracker's title
// and its known alternate titles. 1.0 is a normalized exact match with no
// suspect markers; anything below is token overlap (Dice coefficient) against
// the best of the wanted titles, discounted when the candidate carries a
// derivative-work marker the wanted titles don't.
func ScoreCandidate(candidateTitle string, wantedTitles []string) float64 {
	normalizedCandidate := searchutil.Normalize(candidateTitle)
	if normalizedCandidate == "" {
		return 0
	}

	best := 0.0
	exact := false
	for _, wanted := range wantedTitles {
		normalizedWanted := searchutil.Normalize(wanted)
		if normalizedWanted == "" {
			continue
		}
		if normalizedCandidate == normalizedWanted {
			exact = true
			best = 1.0
			break
		}
		if overlap := tokenDice(normalizedCandidate, normalizedWanted); overlap > best {
			best = overlap
		}
	}

	if penalty := markerPenalty(normalizedCandidate, wantedTitles); penalty {
		// An exact-with-marker cannot happen (the marker word would break
		// equality), but a near-exact "title colored" can — and must not be
		// auto-acceptable.
		best *= 0.6
	}
	if exact {
		return 1.0
	}
	return best
}

// markerPenalty reports whether the candidate carries a derivative-work marker
// that none of the wanted titles carry.
func markerPenalty(normalizedCandidate string, wantedTitles []string) bool {
	for _, marker := range suspectMarkers {
		if !containsMarker(normalizedCandidate, marker) {
			continue
		}
		wantedHasMarker := false
		for _, wanted := range wantedTitles {
			if containsMarker(searchutil.Normalize(wanted), marker) {
				wantedHasMarker = true
				break
			}
		}
		if !wantedHasMarker {
			return true
		}
	}
	return false
}

// containsMarker matches on token boundaries: "dj" must flag "Blue Lock dj"
// without flagging every title that merely contains those letters.
func containsMarker(normalized string, marker string) bool {
	return strings.Contains(" "+normalized+" ", " "+marker+" ")
}

// tokenDice is the Dice coefficient over the two titles' token sets: a cheap,
// order-insensitive overlap measure that behaves sanely when one title has a
// subtitle the other lacks.
func tokenDice(normalizedA, normalizedB string) float64 {
	tokensA := searchutil.TokenizeNormalized(normalizedA)
	tokensB := searchutil.TokenizeNormalized(normalizedB)
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0
	}

	setA := make(map[string]struct{}, len(tokensA))
	for _, token := range tokensA {
		setA[token] = struct{}{}
	}
	shared := 0
	for _, token := range tokensB {
		if _, ok := setA[token]; ok {
			shared++
		}
	}

	return 2 * float64(shared) / float64(len(tokensA)+len(tokensB))
}
