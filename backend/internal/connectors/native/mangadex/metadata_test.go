package mangadex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

const metadataTitleID = "e4121f7e-41c1-448f-98df-a019d764c17a"

// daraSanRecord is the live /manga/{id} answer for Reiwa no Dara-san
// (2026-11), trimmed to the fields the connector reads plus a few it must
// ignore: a Spanish description, format and content tags, a related-manga
// relationship without attributes, and an author biography.
const daraSanRecord = `{"result":"ok","response":"entity","data":{"id":"` + metadataTitleID + `","type":"manga",
	"attributes":{"title":{"ja-ro":"Reiwa no Dara-san"},"altTitles":[{"en":"Dara-san of Reiwa"}],
		"description":{"en":"On a stormy day, a landslide destroys a fence…\n\n“I'm a curse god, you know!?”\n___\n**Additional Links:**\n- [Official X](https://twitter.com/darasan_PR)","es":"En un día de tormenta..."},
		"links":{"al":"160722","mal":"160544"},"originalLanguage":"ja","lastVolume":"","lastChapter":"",
		"publicationDemographic":"seinen","status":"ongoing","year":2022,"contentRating":"suggestive",
		"tags":[
			{"id":"4d32cc48","type":"tag","attributes":{"name":{"en":"Comedy"},"description":{},"group":"genre","version":1},"relationships":[]},
			{"id":"9ab53f92","type":"tag","attributes":{"name":{"en":"Crossdressing"},"description":{},"group":"theme","version":1},"relationships":[]},
			{"id":"b29d6a3d","type":"tag","attributes":{"name":{"en":"Gore"},"description":{},"group":"content","version":1},"relationships":[]},
			{"id":"cdad7e68","type":"tag","attributes":{"name":{"en":"Horror"},"description":{},"group":"genre","version":1},"relationships":[]},
			{"id":"3e2b8dae","type":"tag","attributes":{"name":{"en":"Long Strip"},"description":{},"group":"format","version":1},"relationships":[]},
			{"id":"eabc5b4c","type":"tag","attributes":{"name":{"en":"Supernatural"},"description":{},"group":"theme","version":1},"relationships":[]}
		],"state":"published"},
	"relationships":[
		{"id":"229dc85e","type":"author","attributes":{"name":"Tomotsuka Haruomi","imageUrl":null,"biography":{"en":"**Circle:** Rapid Rabbit"},"twitter":"https://twitter.com/TomotukaHaruomi"}},
		{"id":"229dc85e","type":"artist","attributes":{"name":"Tomotsuka Haruomi","imageUrl":null,"biography":{"en":"**Circle:** Rapid Rabbit"}}},
		{"id":"231ce9d9","type":"cover_art","attributes":{"description":"","volume":"8","fileName":"0d86020e.jpg","locale":"ja"}},
		{"id":"4702d8c5","type":"manga","related":"doujinshi"},
		{"id":"80290487","type":"creator"}
	]}}`

// serveMetadataTitle answers the by-id request with record and an empty feed,
// recording the includes[] each by-id request asked for.
func serveMetadataTitle(t *testing.T, record string, includes *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+metadataTitleID, func(w http.ResponseWriter, r *http.Request) {
		if includes != nil {
			*includes = r.URL.Query()["includes[]"]
		}
		_, _ = w.Write([]byte(record))
	})
	mux.HandleFunc("/manga/"+metadataTitleID+"/feed", serveFeed([]feedEntry{
		{id: "c54", chapter: "54", publish: "2026-11-07T00:00:00+00:00"},
	}, nil))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// The by-id request the resolve already makes carries the series' metadata once
// it also expands the author and artist relationships. Tags are split by group
// (genre and theme kept, format and content dropped), the description is the
// English one cut at MangaDex's links-after-a-rule convention, and the
// demographic is capitalised like every other site's.
func TestMangaDexResolveReadsSeriesMetadata(t *testing.T) {
	var includes []string
	server := serveMetadataTitle(t, daraSanRecord, &includes)
	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	for _, want := range []string{"cover_art", "author", "artist"} {
		if !slices.Contains(includes, want) {
			t.Fatalf("by-id request includes %v, missing %q", includes, want)
		}
	}
	if resolved.CoverImageURL != coverCDNURL+"/covers/"+metadataTitleID+"/0d86020e.jpg.256.jpg" {
		t.Fatalf("expanding authors must not disturb the cover, got %q", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 54 {
		t.Fatalf("expected the feed's chapter 54, got %v", resolved.LatestChapter)
	}

	want := &connectors.SeriesMetadata{
		Type:          connectors.SeriesTypeManga,
		Status:        connectors.SeriesStatusOngoing,
		Year:          2022,
		Genres:        []string{"Comedy", "Horror"},
		Tags:          []string{"Crossdressing", "Supernatural"},
		Demographic:   "Seinen",
		ContentRating: connectors.ContentRatingSuggestive,
		Description:   "On a stormy day, a landslide destroys a fence…\n\n“I'm a curse god, you know!?”",
		Authors:       []string{"Tomotsuka Haruomi"},
		Artists:       []string{"Tomotsuka Haruomi"},
	}
	if !reflect.DeepEqual(resolved.Metadata, want) {
		gotJSON, _ := json.Marshal(resolved.Metadata)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("metadata mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

// A description with no English entry is left empty rather than filled from
// another language, and a null demographic and year stay unset.
func TestMangaDexMetadataTakesOnlyEnglishDescription(t *testing.T) {
	record := `{"data":{"id":"` + metadataTitleID + `","attributes":{"title":{"en":"Untranslated"},
		"description":{"es":"Solo en español.","ja":"日本語"},"originalLanguage":"zh-hk",
		"publicationDemographic":null,"year":null,"status":"hiatus"},"relationships":[]}}`
	server := serveMetadataTitle(t, record, nil)
	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	want := &connectors.SeriesMetadata{Type: connectors.SeriesTypeManhua, Status: connectors.SeriesStatusHiatus}
	if !reflect.DeepEqual(resolved.Metadata, want) {
		t.Fatalf("metadata = %+v, want %+v", resolved.Metadata, want)
	}
}

// A metadata field that changes shape costs that field alone: the record is
// what the resolve rests on (its title, cover and lastChapter fallback), so a
// description sent as [], a year as a string, a tag that is not an object or an
// author name that is not a string must never fail the decode.
func TestMangaDexMalformedMetadataNeverFailsResolve(t *testing.T) {
	record := `{"data":{"id":"` + metadataTitleID + `","attributes":{"title":{"en":"Test Title"},"lastChapter":"42",
		"description":[],"year":"2019","status":7,"originalLanguage":{"code":"ja"},"contentRating":["safe"],
		"publicationDemographic":12,
		"tags":[42,"Action",{"attributes":{"name":[],"group":"genre"}},{"attributes":{"name":{"en":"Drama"},"group":"genre"}},{"attributes":{"name":{"en":7},"group":"theme"}}]},
		"relationships":[{"type":"author","attributes":{"name":{"en":"Nobody"}}},{"type":"artist","attributes":{"name":"Real Artist"}},
		{"type":"cover_art","attributes":{"fileName":"cover.jpg"}}]}}`
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+metadataTitleID, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(record))
	})
	mux.HandleFunc("/manga/"+metadataTitleID+"/feed", serveFeed(nil, nil))
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID)
	if err != nil {
		t.Fatalf("malformed metadata must not fail the resolve: %v", err)
	}
	if resolved.Title != "Test Title" || resolved.LatestChapter == nil || *resolved.LatestChapter != 42 || resolved.CoverImageURL == "" {
		t.Fatalf("expected title, record chapter and cover to resolve as usual, got %+v", resolved)
	}

	want := &connectors.SeriesMetadata{
		Year:    2019,
		Genres:  []string{"Drama"},
		Artists: []string{"Real Artist"},
	}
	if !reflect.DeepEqual(resolved.Metadata, want) {
		gotJSON, _ := json.Marshal(resolved.Metadata)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("metadata mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

// A record without any metadata field reports none, rather than an empty
// struct a consumer would have to recognise as empty.
func TestMangaDexRecordWithoutMetadataReportsNone(t *testing.T) {
	record := `{"data":{"id":"` + metadataTitleID + `","attributes":{"title":{"en":"Bare"},"description":{},"tags":[]},"relationships":[]}}`
	server := serveMetadataTitle(t, record, nil)
	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Metadata != nil {
		t.Fatalf("expected no metadata from a bare record, got %+v", resolved.Metadata)
	}
}

func TestDemographicLabel(t *testing.T) {
	cases := map[string]string{
		"shounen":    "Shounen",
		"seinen":     "Seinen",
		" josei ":    "Josei",
		"none":       "",
		"":           "",
		"Shoujo":     "Shoujo",
		"émigré":     "Émigré",
		"unknown":    "",
		"  \t  ":     "",
		"kodomomuke": "Kodomomuke",
	}
	for raw, want := range cases {
		if got := demographicLabel(raw); got != want {
			t.Fatalf("demographicLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}
