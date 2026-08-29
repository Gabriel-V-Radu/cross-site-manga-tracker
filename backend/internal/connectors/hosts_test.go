package connectors

import "testing"

func TestHostAllowed(t *testing.T) {
	mangafire := []string{"mangafire.to"}
	asura := []string{"asuracomic.net", "asurascans.com"}

	cases := []struct {
		name    string
		host    string
		allowed []string
		want    bool
	}{
		{name: "exact match", host: "mangafire.to", allowed: mangafire, want: true},
		{name: "www subdomain", host: "www.mangafire.to", allowed: mangafire, want: true},
		{name: "deep subdomain", host: "cdn.img.mangafire.to", allowed: mangafire, want: true},
		{name: "second entry in the list", host: "www.asurascans.com", allowed: asura, want: true},
		{
			// The suffix trap: a lookalike domain someone else owns must never
			// be treated as ours, which is why the check demands the dot.
			name:    "lookalike domain is not a subdomain",
			host:    "notmangafire.to",
			allowed: mangafire,
			want:    false,
		},
		{
			// The other half of the trap: our name as a prefix of someone
			// else's domain.
			name:    "our name as a label of another domain",
			host:    "mangafire.to.evil.example",
			allowed: mangafire,
			want:    false,
		},
		{name: "unrelated host", host: "example.com", allowed: mangafire, want: false},
		{name: "sibling tld is a different site", host: "mangafire.net", allowed: mangafire, want: false},
		{name: "empty host", host: "", allowed: mangafire, want: false},
		{name: "whitespace host", host: "   ", allowed: mangafire, want: false},
		{name: "empty allow list", host: "mangafire.to", allowed: nil, want: false},
		{
			// A blank entry (an unset config value, a stray comma in a list) is
			// skipped rather than matching everything.
			name:    "blank entries are skipped",
			host:    "mangafire.to",
			allowed: []string{"", "   ", "mangafire.to"},
			want:    true,
		},
		{
			name:    "a list of only blank entries matches nothing",
			host:    "mangafire.to",
			allowed: []string{"", "  "},
			want:    false,
		},
		{
			// A trailing-dot host would end with "." and could otherwise be
			// matched by a blank entry.
			name:    "fully qualified trailing dot against a blank entry",
			host:    "mangafire.to.",
			allowed: []string{""},
			want:    false,
		},
		{name: "host case is ignored", host: "WWW.MangaFire.TO", allowed: mangafire, want: true},
		{name: "allow list case is ignored", host: "www.mangafire.to", allowed: []string{"MangaFire.TO"}, want: true},
		{name: "host padding is ignored", host: "  mangafire.to\n", allowed: mangafire, want: true},
		{name: "allow list padding is ignored", host: "mangafire.to", allowed: []string{" mangafire.to "}, want: true},
		{
			// Callers must pass url.Hostname(), not url.Host: a port makes the
			// host a different string and the site stops being recognized.
			name:    "host carrying a port does not match",
			host:    "mangafire.to:443",
			allowed: mangafire,
			want:    false,
		},
		{
			// The same rule as the trailing dot above, against a real entry.
			name:    "fully qualified trailing dot does not match",
			host:    "mangafire.to.",
			allowed: mangafire,
			want:    false,
		},
		{
			// A leading dot satisfies the subdomain suffix, so a malformed host
			// of this shape is still accepted. Harmless here: it can only
			// widen the match to a host that is already ours.
			name:    "leading dot passes as a subdomain",
			host:    ".mangafire.to",
			allowed: mangafire,
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostAllowed(tc.host, tc.allowed); got != tc.want {
				t.Fatalf("HostAllowed(%q, %v) = %t, want %t", tc.host, tc.allowed, got, tc.want)
			}
		})
	}
}

// TestReaderRankOrdering pins the tiers the dashboard's reading chain sorts by:
// lower is better, and the info floor must stay last so a source nobody wants
// to read on never wins over a real reader.
func TestReaderRankOrdering(t *testing.T) {
	if ReaderRankOrigin >= ReaderRankFreshAggregator ||
		ReaderRankFreshAggregator >= ReaderRankDefault ||
		ReaderRankDefault >= ReaderRankInfoFloor {
		t.Fatalf("reader ranks are out of order: origin=%d fresh=%d default=%d floor=%d",
			ReaderRankOrigin, ReaderRankFreshAggregator, ReaderRankDefault, ReaderRankInfoFloor)
	}
	// A connector that publishes no rank at all gets Go's zero value, which is
	// the origin tier — the registry's own tests rely on the constants being
	// explicit rather than positional.
	if ReaderRankOrigin != 0 {
		t.Fatalf("ReaderRankOrigin = %d, want 0", ReaderRankOrigin)
	}
}
