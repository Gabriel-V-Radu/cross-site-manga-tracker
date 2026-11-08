package metadata

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func ratingOf(value float64) *float64 { return &value }

func TestConsolidateEmpty(t *testing.T) {
	if got := Consolidate(nil); !got.Empty() {
		t.Fatalf("Consolidate(nil) = %#v, want empty", got)
	}
}

func TestConsolidateMergesAcrossSources(t *testing.T) {
	entries := []SourceEntry{
		{SourceKey: "mangahub", SourceName: "MangaHub", Metadata: connectors.SeriesMetadata{
			Status:      "ongoing",
			Genres:      []string{"Action", "Martial Arts", "Long Strip", "Web Comic", "Full Color", "Safe"},
			Description: "Short.",
			Authors:     []string{"Bug Mihon"},
		}},
		{SourceKey: "mangafire", SourceName: "MangaFire", Metadata: connectors.SeriesMetadata{
			Type:        "manhwa",
			Status:      "completed",
			Year:        2022,
			Genres:      []string{"Action", "Fantasy"},
			Tags:        []string{"Reincarnation", "Monsters"},
			Demographic: "Seinen",
			Description: strings.Repeat("A long synopsis from MangaFire. ", 4),
			Authors:     []string{"TOMOTSUKA Haruomi"},
			Rating:      ratingOf(9.4),
			RatingVotes: 57,
		}},
		{SourceKey: "mangaupdates", SourceName: "MangaUpdates", Metadata: connectors.SeriesMetadata{
			Type:    "manhwa",
			Year:    2021,
			Genres:  []string{"Action", "Sci-fi", "Shounen", "Shounen Ai"},
			Tags:    []string{"Regression", "Male Lead without Sexual Experience"},
			Authors: []string{"Tomotsuka Haruomi"},
			Rating:  ratingOf(7.9),
		}},
	}

	got := Consolidate(entries)

	if got.Type != "manhwa" {
		t.Errorf("Type = %q", got.Type)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want MangaFire's", got.Status)
	}
	if got.Year != 2021 {
		t.Errorf("Year = %d, want MangaUpdates'", got.Year)
	}
	if got.Demographic != "Seinen" {
		t.Errorf("Demographic = %q", got.Demographic)
	}
	if !strings.HasPrefix(got.Description, "A long synopsis") || got.DescriptionSource != "MangaFire" {
		t.Errorf("Description from %q = %q", got.DescriptionSource, got.Description)
	}
	if !reflect.DeepEqual(got.Authors, []string{"Tomotsuka Haruomi"}) {
		t.Errorf("Authors = %#v, want MangaUpdates' only", got.Authors)
	}
	if got.Genres[0] != "Action" {
		t.Errorf("Genres should lead with the label every site agrees on: %#v", got.Genres)
	}
	for _, want := range []string{"Martial Arts", "Fantasy", "Sci-Fi", "Boys' Love", "Reincarnation", "Monsters", "Time Travel"} {
		if !contains(got.Genres, want) {
			t.Errorf("Genres %#v missing %q", got.Genres, want)
		}
	}
	for _, unwanted := range []string{"Long Strip", "Safe", "Shounen", "Web Comic"} {
		if contains(got.Genres, unwanted) || contains(got.Tags, unwanted) {
			t.Errorf("%q leaked into genres/tags: %#v / %#v", unwanted, got.Genres, got.Tags)
		}
	}
	if !reflect.DeepEqual(got.Tags, []string{"Male Lead without Sexual Experience"}) {
		t.Errorf("Tags = %#v", got.Tags)
	}
	if len(got.Ratings) != 2 || got.Ratings[0].SourceKey != "mangaupdates" || got.Ratings[1].Votes != 57 {
		t.Errorf("Ratings = %#v", got.Ratings)
	}
	if !reflect.DeepEqual(got.Sources, []string{"MangaUpdates", "MangaFire", "MangaHub"}) {
		t.Errorf("Sources = %#v", got.Sources)
	}
}

func TestConsolidateTypeIsVoted(t *testing.T) {
	entries := []SourceEntry{
		{SourceKey: "mangaupdates", Metadata: connectors.SeriesMetadata{Type: "novel"}},
		{SourceKey: "mangafire", Metadata: connectors.SeriesMetadata{Type: "manhwa"}},
		{SourceKey: "comick", Metadata: connectors.SeriesMetadata{Type: "manhwa"}},
	}
	if got := Consolidate(entries).Type; got != "manhwa" {
		t.Fatalf("Type = %q, want the majority's manhwa", got)
	}

	tie := []SourceEntry{
		{SourceKey: "asuracomic", Metadata: connectors.SeriesMetadata{Type: "manhua"}},
		{SourceKey: "flamecomics", Metadata: connectors.SeriesMetadata{Type: "manhwa"}},
	}
	first := Consolidate(tie).Type
	second := Consolidate([]SourceEntry{tie[1], tie[0]}).Type
	if first != second {
		t.Fatalf("a tie must not depend on input order: %q vs %q", first, second)
	}
}

func TestConsolidateShortDescriptionLosesToLongerOne(t *testing.T) {
	long := strings.Repeat("Asura wrote a proper synopsis here. ", 3)
	entries := []SourceEntry{
		{SourceKey: "mangadex", SourceName: "MangaDex", Metadata: connectors.SeriesMetadata{Description: "Her story."}},
		{SourceKey: "asuracomic", SourceName: "AsuraComic", Metadata: connectors.SeriesMetadata{Description: long}},
	}
	got := Consolidate(entries)
	if got.Description != strings.TrimSpace(long) || got.DescriptionSource != "AsuraComic" {
		t.Fatalf("Description from %q = %q", got.DescriptionSource, got.Description)
	}
}

func TestConsolidateTagsAreCapped(t *testing.T) {
	tags := make([]string, 0, 30)
	for index := 0; index < 30; index++ {
		tags = append(tags, "Category "+string(rune('A'+index)))
	}
	got := Consolidate([]SourceEntry{{SourceKey: "mangaupdates", Metadata: connectors.SeriesMetadata{Tags: tags}}})
	if len(got.Tags) != maxTags {
		t.Fatalf("len(Tags) = %d, want %d", len(got.Tags), maxTags)
	}
}

func TestClassifyLabel(t *testing.T) {
	cases := []struct {
		label     string
		class     labelClass
		canonical string
	}{
		{"Sci-fi", labelGenre, "Sci-Fi"},
		{"Boys’ Love", labelGenre, "Boys' Love"},
		{"MURIM", labelGenre, "Martial Arts"},
		{"Shonen", labelDemographic, "Shounen"},
		{"Long Strip", labelIgnored, ""},
		{"ROMANCE", labelGenre, "Romance"},
		{"office workers", labelTag, "Office Workers"},
		{"Male Lead without Sexual Experience", labelTag, "Male Lead without Sexual Experience"},
	}
	for _, tc := range cases {
		class, canonical := classifyLabel(tc.label)
		if class != tc.class || canonical != tc.canonical {
			t.Errorf("classifyLabel(%q) = %v %q, want %v %q", tc.label, class, canonical, tc.class, tc.canonical)
		}
	}
}

func TestCanonicalGenresAreSortedAndUnique(t *testing.T) {
	genres := CanonicalGenres()
	for index := 1; index < len(genres); index++ {
		if genres[index-1] >= genres[index] {
			t.Fatalf("not sorted/unique at %d: %q, %q", index, genres[index-1], genres[index])
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
