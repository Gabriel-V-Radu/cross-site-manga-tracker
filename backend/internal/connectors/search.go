package connectors

import (
	"fmt"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// SearchQuery is a title search after the preparation every connector's
// SearchByTitle used to open with by hand: trimmed, normalized, tokenized, its
// limit clamped. Matches applies the shared post-filter, so a site whose search
// answers loosely (MangaDex, MangaFire) returns only what the query asked for.
type SearchQuery struct {
	// Raw is the trimmed title as the caller spelled it — what the site's own
	// search endpoint is asked.
	Raw string
	// Normalized and Tokens are the searchutil forms the post-filter compares
	// candidates against.
	Normalized string
	Tokens     []string
	// Limit is the clamped result cap (ClampSearchLimit).
	Limit int
}

// PrepareSearch validates and normalizes a title query. A title that is empty,
// or reduces to nothing once punctuation is stripped ("!!!"), is refused here
// so no connector forwards it to a site.
func PrepareSearch(title string, limit int) (SearchQuery, error) {
	raw := strings.TrimSpace(title)
	if raw == "" {
		return SearchQuery{}, fmt.Errorf("title is required")
	}
	normalized := searchutil.Normalize(raw)
	tokens := searchutil.TokenizeNormalized(normalized)
	if normalized == "" || len(tokens) == 0 {
		return SearchQuery{}, fmt.Errorf("title is required")
	}
	return SearchQuery{
		Raw:        raw,
		Normalized: normalized,
		Tokens:     tokens,
		Limit:      ClampSearchLimit(limit),
	}, nil
}

// Matches reports whether any candidate title answers the query: it contains
// the whole normalized query, or every one of its tokens.
func (q SearchQuery) Matches(candidates ...string) bool {
	return searchutil.AnyCandidateMatches(candidates, q.Normalized, q.Tokens)
}
