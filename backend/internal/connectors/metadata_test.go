package connectors

import (
	"strings"
	"testing"
)

func TestNormalizeSeriesType(t *testing.T) {
	cases := map[string]string{
		"Manhwa":      SeriesTypeManhwa,
		"MANHUA":      SeriesTypeManhua,
		"manga":       SeriesTypeManga,
		"one_shot":    SeriesTypeManga,
		"light_novel": SeriesTypeNovel,
		"Novel":       SeriesTypeNovel,
		"OEL":         SeriesTypeComic,
		"Doujinshi":   SeriesTypeOther,
		"Webtoon":     "",
		"":            "",
	}
	for raw, want := range cases {
		if got := NormalizeSeriesType(raw); got != want {
			t.Errorf("NormalizeSeriesType(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestSeriesTypeFromLanguageAndCountry(t *testing.T) {
	if got := SeriesTypeFromLanguage("zh-hk"); got != SeriesTypeManhua {
		t.Errorf("zh-hk = %q", got)
	}
	if got := SeriesTypeFromLanguage("ko"); got != SeriesTypeManhwa {
		t.Errorf("ko = %q", got)
	}
	if got := SeriesTypeFromCountry("KR"); got != SeriesTypeManhwa {
		t.Errorf("KR = %q", got)
	}
	if got := SeriesTypeFromCountry("jp"); got != SeriesTypeManga {
		t.Errorf("jp = %q", got)
	}
	if got := SeriesTypeFromCountry("fr"); got != "" {
		t.Errorf("fr = %q", got)
	}
}

func TestNormalizeSeriesStatus(t *testing.T) {
	cases := map[string]string{
		"releasing":               SeriesStatusOngoing,
		"OnGoing":                 SeriesStatusOngoing,
		"135 Chapters (Ongoing)":  SeriesStatusOngoing,
		"Finished":                SeriesStatusCompleted,
		"Completed":               SeriesStatusCompleted,
		"10 Volumes (Complete)":   SeriesStatusCompleted,
		"on_hiatus":               SeriesStatusHiatus,
		"discontinued":            SeriesStatusCancelled,
		"Cancelled":               SeriesStatusCancelled,
		"":                        "",
		"something else entirely": "",
	}
	for raw, want := range cases {
		if got := NormalizeSeriesStatus(raw); got != want {
			t.Errorf("NormalizeSeriesStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestPlainDescriptionHTMLAndBBCode(t *testing.T) {
	raw := "From Yen Press:  <br>\nClouds blacken the sky &amp; rain falls.<br><br><br>[i]A [b]long[/b] night.[/i]"
	got := PlainDescription(raw)
	want := "From Yen Press:\nClouds blacken the sky & rain falls.\n\nA long night."
	if got != want {
		t.Fatalf("PlainDescription = %q, want %q", got, want)
	}
}

func TestPlainDescriptionMarkdownCutsAtRule(t *testing.T) {
	raw := "Her name is **Mimi**, see [her page](https://example.com/a_(b)).\n\n---\n**Links:**\n- [Official](https://x)"
	got := PlainDescription(raw)
	want := "Her name is Mimi, see her page."
	if got != want {
		t.Fatalf("PlainDescription = %q, want %q", got, want)
	}
}

func TestPlainDescriptionCutsComicKInlineRule(t *testing.T) {
	raw := "Shin Youngwoo finds a powerful item and life as a legend may be more than he bargained for….---Alternative Official English: [WebToon](https://www.webtoons.com/x)"
	got := PlainDescription(raw)
	if strings.Contains(got, "Alternative") || !strings.HasSuffix(got, "bargained for….") {
		t.Fatalf("PlainDescription = %q", got)
	}
}

func TestPlainDescriptionBounded(t *testing.T) {
	got := PlainDescription(strings.Repeat("word ", 2000))
	if runes := []rune(got); len(runes) > maxDescriptionRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("description not bounded: %d runes", len(runes))
	}
}

func TestCleanLabelsAndSplit(t *testing.T) {
	got := SplitLabels("Action, Martial Arts; action|  Web Comic ,updating,")
	want := []string{"Action", "Martial Arts", "Web Comic"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("SplitLabels = %#v, want %#v", got, want)
	}
	if CleanLabels([]string{" ", "updating"}) != nil {
		t.Fatal("CleanLabels of placeholders should be nil")
	}
}

func TestParseRating(t *testing.T) {
	if got := ParseRating("8.45", 10); got == nil || *got != 8.45 {
		t.Fatalf("ParseRating(8.45) = %v", got)
	}
	if got := ParseRating("4.7", 5); got == nil || *got != 9.4 {
		t.Fatalf("ParseRating(4.7/5) = %v", got)
	}
	for _, raw := range []string{"", "0", "-1", "11", "abc", "NaN", "Inf"} {
		if got := ParseRating(raw, 10); got != nil {
			t.Fatalf("ParseRating(%q) = %v, want nil", raw, *got)
		}
	}
}

func TestSeriesMetadataEmpty(t *testing.T) {
	var nilMeta *SeriesMetadata
	if !nilMeta.Empty() || !(&SeriesMetadata{}).Empty() {
		t.Fatal("zero metadata should be empty")
	}
	if (&SeriesMetadata{Genres: []string{"Action"}}).Empty() {
		t.Fatal("metadata with a genre is not empty")
	}
}
