package searchutil

import (
	"reflect"
	"testing"
)

// These tests regression-lock the CURRENT behavior of the title
// normalization/matching helpers. Roughly twenty callers (every connector's
// title matching, linkscan scoring, repository search) depend on these exact
// semantics; a silent change here mislinks trackers across sites.

func TestNormalize(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "whitespace only", input: "   \t  ", want: ""},
		{name: "lowercases and trims", input: "  One Piece  ", want: "one piece"},
		{name: "collapses internal whitespace", input: "one\t\tpiece   red", want: "one piece red"},
		{name: "colon becomes space", input: "Solo Leveling: Season 2", want: "solo leveling season 2"},
		{name: "period becomes space", input: "Dr. Stone", want: "dr stone"},
		{name: "hyphen becomes space", input: "Kubo-san", want: "kubo san"},
		{name: "straight apostrophe becomes space", input: "Kubo-san's Class", want: "kubo san s class"},
		{name: "brackets and punctuation become spaces", input: "[Oshi no Ko] (Official)", want: "oshi no ko official"},
		{name: "slash pipe plus equals hash ampersand asterisk", input: "a/b|c+d=e#f&g*h", want: "a b c d e f g h"},
		{name: "punctuation only collapses to empty", input: "!!!???...", want: ""},
		// Articles are NOT stripped -- "the"/"a" remain load-bearing tokens.
		{name: "articles are kept", input: "The Beginning After the End", want: "the beginning after the end"},
		// Only ASCII punctuation in the replacer list is substituted. Unicode
		// punctuation passes through untouched (and can glue words together
		// into a single token).
		{name: "curly apostrophe is kept", input: "It’s Mine", want: "it’s mine"},
		{name: "em dash is kept", input: "Solo—Leveling", want: "solo—leveling"},
		{name: "percent tilde at caret are kept", input: "100% ~Power~ @Home ^Up", want: "100% ~power~ @home ^up"},
		{name: "cjk passes through", input: "進撃の巨人", want: "進撃の巨人"},
		{name: "accented latin passes through lowercased", input: "Pokémon", want: "pokémon"},
		{name: "digits kept", input: "Chapter 12.5", want: "chapter 12 5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.input); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTokenizeNormalized(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty returns nil", input: "", want: nil},
		{name: "whitespace only returns nil", input: "   ", want: nil},
		{name: "splits on whitespace", input: "one piece red", want: []string{"one", "piece", "red"}},
		{name: "dedupes keeping first occurrence order", input: "one piece one red piece", want: []string{"one", "piece", "red"}},
		// TokenizeNormalized does NOT normalize; callers pass Normalize output.
		{name: "does not lowercase", input: "One PIECE", want: []string{"One", "PIECE"}},
		{name: "single token", input: "berserk", want: []string{"berserk"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenizeNormalized(tc.input); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("TokenizeNormalized(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

func TestMatchesQuery(t *testing.T) {
	cases := []struct {
		name            string
		candidate       string
		normalizedQuery string
		queryTokens     []string
		want            bool
	}{
		{name: "empty candidate never matches", candidate: "", normalizedQuery: "", queryTokens: nil, want: false},
		{name: "candidate normalizing to empty never matches", candidate: "???", normalizedQuery: "x", queryTokens: []string{"x"}, want: false},
		{name: "substring match on normalized query", candidate: "One Piece: Red", normalizedQuery: "piece red", queryTokens: nil, want: true},
		{name: "substring match is order sensitive", candidate: "One Piece", normalizedQuery: "piece one", queryTokens: nil, want: false},
		{name: "punctuation differences ignored via normalization", candidate: "KUBO-SAN'S CLASS", normalizedQuery: "kubo san s", queryTokens: nil, want: true},
		{name: "token fallback requires all tokens", candidate: "One Piece", normalizedQuery: "zzz", queryTokens: []string{"piece", "one"}, want: true},
		{name: "token fallback fails when one token missing", candidate: "One Piece", normalizedQuery: "zzz", queryTokens: []string{"one", "bleach"}, want: false},
		// Token containment is plain substring, not word-boundary: "man"
		// matches inside "romance".
		{name: "token match is substring not word match", candidate: "Romance Dawn", normalizedQuery: "zzz", queryTokens: []string{"man"}, want: true},
		{name: "empty query and no tokens never matches", candidate: "One Piece", normalizedQuery: "", queryTokens: nil, want: false},
		{name: "empty query with tokens uses token path", candidate: "One Piece", normalizedQuery: "", queryTokens: []string{"piece"}, want: true},
		// Quirk locked deliberately: a non-empty token slice containing only
		// empty strings matches every non-empty candidate (each empty token is
		// skipped and the loop falls through to true).
		{name: "all-empty token slice matches any non-empty candidate", candidate: "Anything", normalizedQuery: "zzz", queryTokens: []string{""}, want: true},
		{name: "cjk substring match", candidate: "進撃の巨人 Attack on Titan", normalizedQuery: "進撃の巨人", queryTokens: nil, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchesQuery(tc.candidate, tc.normalizedQuery, tc.queryTokens)
			if got != tc.want {
				t.Fatalf("MatchesQuery(%q, %q, %#v) = %v, want %v", tc.candidate, tc.normalizedQuery, tc.queryTokens, got, tc.want)
			}
		})
	}
}

func TestAnyCandidateMatches(t *testing.T) {
	if AnyCandidateMatches(nil, "one", []string{"one"}) {
		t.Fatalf("nil candidates must not match")
	}
	if AnyCandidateMatches([]string{"Bleach", "Naruto"}, "one piece", []string{"one", "piece"}) {
		t.Fatalf("no candidate contains the query; expected false")
	}
	if !AnyCandidateMatches([]string{"Bleach", "ONE PIECE (Colored)"}, "one piece", []string{"one", "piece"}) {
		t.Fatalf("second candidate matches; expected true")
	}
}

func TestRelatedTitles(t *testing.T) {
	got := RelatedTitles("Nano Machine", []string{
		"Nano Mashin",
		"나노마신",         // not Latin: dropped
		"nano-machine", // the primary under another spelling: dropped
		"Nano Mashin",  // duplicate: dropped
		"  ",
		"NanoMachine",
	})
	want := []string{"Nano Mashin", "NanoMachine"}
	if len(got) != len(want) {
		t.Fatalf("RelatedTitles = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("RelatedTitles = %v, want %v", got, want)
		}
	}

	if RelatedTitles("Nano Machine", []string{"Nano Machine", "나노마신"}) != nil {
		t.Fatal("nothing left must be nil, not an empty slice")
	}
	if RelatedTitles("", []string{"Alpha"}) == nil {
		t.Fatal("an empty primary excludes nothing")
	}
}

func TestUniqueNonEmpty(t *testing.T) {
	t.Run("nil and empty return nil", func(t *testing.T) {
		if got := UniqueNonEmpty(nil); got != nil {
			t.Fatalf("UniqueNonEmpty(nil) = %#v, want nil", got)
		}
		if got := UniqueNonEmpty([]string{}); got != nil {
			t.Fatalf("UniqueNonEmpty([]) = %#v, want nil", got)
		}
	})

	t.Run("dedupes by normalized key keeping first trimmed original", func(t *testing.T) {
		got := UniqueNonEmpty([]string{"  One Piece  ", "one-piece", "ONE. PIECE", "Bleach"})
		want := []string{"One Piece", "Bleach"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("drops blanks and punctuation-only values", func(t *testing.T) {
		got := UniqueNonEmpty([]string{"", "   ", "---", "?!"})
		if len(got) != 0 {
			t.Fatalf("got %#v, want empty", got)
		}
	})

	t.Run("keeps distinct non-ascii titles", func(t *testing.T) {
		got := UniqueNonEmpty([]string{"進撃の巨人", "Attack on Titan", "進撃の巨人"})
		want := []string{"進撃の巨人", "Attack on Titan"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

func TestIsEnglishAlphabetName(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"   ", false},
		{"One Piece", true},
		{"123", false}, // digits alone: no letter
		{"Chapter 12", true},
		{"Kubo-san's Class", true},
		{"Re:Zero", true},
		{"A&B", true},
		{"Fate/stay night", true},
		{"S+ Rank", true},
		{"Who? Me!", true},
		{"[Oshi] {no} (Ko)", true},
		{"semi;colon, comma.", true},
		{"back\\slash", true},
		{"進撃の巨人", false},
		{"Pokémon", false},         // accented latin rejected
		{"It’s Mine", false},       // curly apostrophe rejected (only ASCII ')
		{"Solo — Leveling", false}, // em dash rejected
		{"100% Power", false},      // % not an allowed separator
		{"#1 Hero", false},
		{"*Special*", false},
		{"under_score", false},
		{"a\tb", true}, // any unicode space is allowed
	}

	for _, tc := range cases {
		if got := IsEnglishAlphabetName(tc.input); got != tc.want {
			t.Errorf("IsEnglishAlphabetName(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestFilterEnglishAlphabetNames(t *testing.T) {
	t.Run("nil and empty return nil", func(t *testing.T) {
		if got := FilterEnglishAlphabetNames(nil); got != nil {
			t.Fatalf("got %#v, want nil", got)
		}
	})

	t.Run("filters non-english and dedupes by normalization", func(t *testing.T) {
		got := FilterEnglishAlphabetNames([]string{
			"  Solo Leveling ",
			"나 혼자만 레벨업",
			"solo-leveling",
			"Only I Level Up",
			"",
		})
		want := []string{"Solo Leveling", "Only I Level Up"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}

func TestExtractRelatedTitlesText(t *testing.T) {
	t.Run("empty input returns nil", func(t *testing.T) {
		if got := ExtractRelatedTitles(""); got != nil {
			t.Fatalf("got %#v, want nil", got)
		}
	})

	t.Run("text without alias labels yields nothing", func(t *testing.T) {
		got := ExtractRelatedTitles("A regular synopsis line.\nAnother line: with a colon.")
		if len(got) != 0 {
			t.Fatalf("got %#v, want empty", got)
		}
	})

	t.Run("label line with semicolon delimiter", func(t *testing.T) {
		got := ExtractRelatedTitles("Alternative Titles: Solo Leveling; Only I Level Up")
		want := []string{"Solo Leveling", "Only I Level Up"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("label variants are case insensitive and accept dash", func(t *testing.T) {
		for input, want := range map[string]string{
			"ASSOCIATED NAMES: The Duke":   "The Duke",
			"aliases - Shadow Monarch":     "Shadow Monarch",
			"Other names: Level Up Alone":  "Level Up Alone",
			"Synonyms: The Second Coming":  "The Second Coming",
			"Alternative name: Late Bloom": "Late Bloom",
		} {
			got := ExtractRelatedTitles(input)
			if !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("ExtractRelatedTitles(%q) = %#v, want [%q]", input, got, want)
			}
		}
	})

	t.Run("label-only line collects the next non-empty line and splits commas", func(t *testing.T) {
		got := ExtractRelatedTitles("Associated Names\n\nna guman lebel-eob, 나 혼자만 레벨업\nIgnored trailing line")
		want := []string{"na guman lebel-eob"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("only the first line after a label-with-content line counts", func(t *testing.T) {
		// A continuation line after "Label: value" is NOT collected.
		got := ExtractRelatedTitles("Alternative Names: The Player\nLevel Up Alone")
		want := []string{"The Player"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("html tags line breaks and entities", func(t *testing.T) {
		got := ExtractRelatedTitles(`<div><b>Alternative Names:</b> Ain&#39;t No Hero<br>Second Line Skipped</div>`)
		want := []string{"Ain't No Hero"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("spaced slash splits but embedded slash does not", func(t *testing.T) {
		got := ExtractRelatedTitles("Alternative Titles: Fate/stay night / Fate Stay")
		want := []string{"Fate/stay night", "Fate Stay"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("pipe and bullet delimiters", func(t *testing.T) {
		got := ExtractRelatedTitles("Synonyms: The Duke | His Majesty • Regressor Tale")
		want := []string{"The Duke", "His Majesty", "Regressor Tale"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("non-english aliases are filtered out entirely", func(t *testing.T) {
		got := ExtractRelatedTitles("Alternative Titles: 俺だけレベルアップな件; 나 혼자만 레벨업")
		if len(got) != 0 {
			t.Fatalf("got %#v, want empty", got)
		}
	})
}

func TestExtractRelatedTitlesJSON(t *testing.T) {
	t.Run("json array field", func(t *testing.T) {
		got := ExtractRelatedTitles(`{"altTitles": ["The Greatest Estate Developer", "금수저 개발자", "Gold Spoon Developer"]}`)
		want := []string{"The Greatest Estate Developer", "Gold Spoon Developer"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("json string field split on delimiters", func(t *testing.T) {
		got := ExtractRelatedTitles(`{"synonyms": "A Returner's Magic | The Returner"}`)
		want := []string{"A Returner's Magic", "The Returner"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("snake_case keys and escaped json string decode", func(t *testing.T) {
		// The raw payload carries JSON escapes; decodeJSONString must unquote
		// them (' -> apostrophe) before the English-name filter runs.
		raw := "{\"alt_titles\": [\"Omniscient Reader\\u0027s Viewpoint\"]}"
		got := ExtractRelatedTitles(raw)
		want := []string{"Omniscient Reader's Viewpoint"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("json and text candidates dedupe by normalization with json first", func(t *testing.T) {
		raw := `{"aliases": ["Solo Leveling"]}` + "\nAlternative Titles: solo-leveling; Only I Level Up"
		got := ExtractRelatedTitles(raw)
		want := []string{"Solo Leveling", "Only I Level Up"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
}
