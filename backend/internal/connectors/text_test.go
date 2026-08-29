package connectors

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

func TestFirstSubmatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern *regexp.Regexp
		raw     string
		want    string
	}{
		{
			name:    "captures the group",
			pattern: regexp.MustCompile(`chapter-([0-9.]+)`),
			raw:     `<a href="/manga/nano-machine/chapter-241.5">`,
			want:    "241.5",
		},
		{
			name:    "no match yields empty",
			pattern: regexp.MustCompile(`chapter-([0-9.]+)`),
			raw:     `<a href="/manga/nano-machine">`,
			want:    "",
		},
		{
			// A pattern with no capture group can never return anything, so a
			// connector that drops its parentheses fails closed rather than
			// returning the whole match.
			name:    "pattern without a capture group yields empty",
			pattern: regexp.MustCompile(`chapter-[0-9.]+`),
			raw:     `chapter-241.5`,
			want:    "",
		},
		{
			name:    "only the first group is returned",
			pattern: regexp.MustCompile(`(?i)<title>(.+?)\s+chapter\s+([0-9.]+)</title>`),
			raw:     `<title>Nano Machine Chapter 241</title>`,
			want:    "Nano Machine",
		},
		{
			name:    "first occurrence wins",
			pattern: regexp.MustCompile(`id="([a-z0-9]+)"`),
			raw:     `<div id="first"></div><div id="second"></div>`,
			want:    "first",
		},
		{
			// An empty capture is indistinguishable from no match at all;
			// callers treat "" as absent either way.
			name:    "matching with an empty capture is indistinguishable from no match",
			pattern: regexp.MustCompile(`title="([^"]*)"`),
			raw:     `<div title="">`,
			want:    "",
		},
		{
			name:    "dot matches newlines only when the pattern says so",
			pattern: regexp.MustCompile(`(?s)<h1>(.+?)</h1>`),
			raw:     "<h1>Nano\nMachine</h1>",
			want:    "Nano\nMachine",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FirstSubmatch(tc.pattern, tc.raw); got != tc.want {
				t.Fatalf("FirstSubmatch(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "plain text is untouched", raw: "Nano Machine", want: "Nano Machine"},
		{name: "nested tags", raw: "<div><b>Nano</b> Machine</div>", want: "Nano Machine"},
		{name: "self closing tag", raw: "Nano<br/>Machine", want: "Nano Machine"},
		{name: "tag only", raw: "<br/>", want: ""},
		{name: "entity angle brackets are not markup", raw: "<span class='x'>1 &lt; 2</span>", want: "1 < 2"},
		{name: "html comment", raw: "x<!-- hidden -->y", want: "x y"},
		{name: "tag spanning lines", raw: "<a\nhref='x'>link</a>", want: "link"},
		{name: "collapses runs of whitespace", raw: "  Nano\n\t  Machine  ", want: "Nano Machine"},
		{name: "entities are unescaped", raw: "Solo Leveling &amp; Co&#39;s", want: "Solo Leveling & Co's"},
		{
			// Tags are stripped before entities are unescaped, so script and
			// style bodies survive as text: CleanText takes a fragment, never a
			// whole page.
			name: "script bodies are text, not markup",
			raw:  "<script>var a = 1;</script>ok",
			want: "var a = 1; ok",
		},
		{
			// Only one unescape pass runs, so a double-encoded entity keeps its
			// inner escape.
			name: "single unescape pass",
			raw:  "&amp;amp;",
			want: "&amp;",
		},
		{
			// Because unescaping happens after stripping, a tag the site
			// encoded as entities comes out as visible text rather than being
			// removed.
			name: "entity-encoded tags survive as text",
			raw:  "&lt;b&gt;bold&lt;/b&gt;",
			want: "<b>bold</b>",
		},
		{
			// The tag pattern stops at the first ">", so an attribute value
			// containing ">" leaks its tail into the text.
			name: "malformed attribute leaks its tail",
			raw:  `<div class="a>b">x</div>`,
			want: `b">x`,
		},
		{
			// A bare "<" in prose opens a match that runs to the next ">",
			// swallowing everything between them.
			name: "bare angle brackets swallow the text between them",
			raw:  "a < b and c > d",
			want: "a d",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanText(tc.raw); got != tc.want {
				t.Fatalf("CleanText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestCleanTextNonBreakingSpace: the collapsing pattern is RE2's ASCII \s, so a
// &nbsp; between two words survives as U+00A0 while the outer ones are removed
// (by TrimSpace, which is Unicode-aware). A title scraped from a site that pads
// with &nbsp; therefore keeps an interior non-breaking space, which matters
// wherever such titles are compared as strings.
func TestCleanTextNonBreakingSpace(t *testing.T) {
	got := CleanText("&nbsp;Solo&nbsp;Leveling&nbsp;")
	if want := "Solo\u00a0Leveling"; got != want {
		t.Fatalf("CleanText = %q, want %q", got, want)
	}
	if got == "Solo Leveling" {
		t.Fatal("an interior non-breaking space is not collapsed to an ASCII space")
	}
}

func TestPrettifySlug(t *testing.T) {
	cases := []struct {
		name string
		slug string
		want string
	}{
		{name: "dashes become spaces", slug: "nano-machine", want: "Nano Machine"},
		{name: "empty stays empty", slug: "", want: ""},
		{name: "dashes only", slug: "---", want: ""},
		{name: "whitespace only", slug: "   ", want: ""},
		{name: "surrounding whitespace is trimmed", slug: "  the-100-girlfriends  ", want: "The 100 Girlfriends"},
		{name: "repeated dashes collapse", slug: "solo--leveling", want: "Solo Leveling"},
		{name: "leading digits are left alone", slug: "100-girlfriends", want: "100 Girlfriends"},
		{name: "already spaced and capitalized", slug: "Nano Machine", want: "Nano Machine"},
		{name: "existing capitals inside a word are kept", slug: "reMonster", want: "ReMonster"},
		// Underscore slugs are not split, so only the first word is capitalized.
		{name: "underscores are not separators", slug: "nano_machine", want: "Nano_machine"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrettifySlug(tc.slug); got != tc.want {
				t.Fatalf("PrettifySlug(%q) = %q, want %q", tc.slug, got, tc.want)
			}
		})
	}
}

// TestPrettifySlugCorruptsNonASCIIFirstRune pins current behavior, which is a
// bug: capitalization slices the first BYTE of each word, so a word starting
// with a multi-byte rune loses that rune's lead byte to U+FFFD and leaves its
// continuation bytes orphaned — the result is not valid UTF-8. A word whose
// first byte is ASCII is unaffected, which is why the accent inside "siege"
// survives below. Every site currently linked uses ASCII slugs, so nothing in
// the app reaches this today.
func TestPrettifySlugCorruptsNonASCIIFirstRune(t *testing.T) {
	// The slug is a French title with accents, spelled in escapes here so the
	// expected mojibake stays readable in the source.
	got := PrettifySlug("\u00e9tat-de-si\u00e8ge")
	if want := "\uFFFD\xa9tat De Si\u00e8ge"; got != want {
		t.Fatalf("PrettifySlug = %q, want %q", got, want)
	}
	if utf8.ValidString(got) {
		t.Fatal("expected the current byte-slicing behavior to produce invalid UTF-8")
	}
	if PrettifySlug("nano-machine") != "Nano Machine" {
		t.Fatal("ASCII slugs must be unaffected")
	}
}
