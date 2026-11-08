package mangafire

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func float64Pointer(value float64) *float64 {
	return &value
}

// The title record the resolve already downloads describes the series, so the
// resolve hands that description back: genres and themes kept apart, the first
// demographic, the synopsis reduced to plain text and the 0-10 rating as is.
func TestMangaFireResolveReadsSeriesMetadata(t *testing.T) {
	server := newFakeAPIServer(t)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	want := &connectors.SeriesMetadata{
		Type:          connectors.SeriesTypeManga,
		Status:        connectors.SeriesStatusOngoing,
		Year:          1997,
		Genres:        []string{"Action", "Adventure", "Comedy"},
		Tags:          []string{"Pirates", "Super Power"},
		Demographic:   "Shounen",
		ContentRating: connectors.ContentRatingSafe,
		Description:   "Gol D. Roger was known as the \"Pirate King\".\nHis last words sent the world to sea.\n\nThe Great Pirate Era begins.",
		Authors:       []string{"ODA Eiichiro"},
		Artists:       []string{"Oda Eiichiro"},
		Rating:        float64Pointer(9.12),
		RatingVotes:   4821,
	}
	if !reflect.DeepEqual(resolved.Metadata, want) {
		gotJSON, _ := json.Marshal(resolved.Metadata)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("metadata mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

// A metadata field that changes shape costs that field alone. The title record
// is what the resolve rests on — its hid, slug and title — so a number sent as a
// string, an object where a list was or a null must never fail the decode, and
// the fields that still read correctly are kept.
func TestMangaFireMalformedMetadataNeverFailsResolve(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece",
			"type":5,"status":{"code":"releasing"},"year":"1997","contentRating":null,
			"rating":"9.4","ratingCount":"many","synopsisHtml":["not","html"],
			"genres":{"id":1,"title":"Action"},"themes":"Pirates","demographics":null,
			"authors":[{"id":7,"title":"ODA Eiichiro"},42,{"id":9,"title":null},{"id":10,"name":"Ghost Writer"}],
			"artists":[[]]}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":75117,"number":1187,"language":"en","createdAt":1783047602}],"meta":{"hasNext":false}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("malformed metadata must not fail the resolve: %v", err)
	}
	if resolved.Title != "One Piece" || resolved.LatestChapter == nil || *resolved.LatestChapter != 1187 {
		t.Fatalf("expected the title and chapter to resolve as usual, got %q chapter %v", resolved.Title, resolved.LatestChapter)
	}

	want := &connectors.SeriesMetadata{
		Year:    1997,
		Authors: []string{"ODA Eiichiro", "Ghost Writer"},
		Rating:  float64Pointer(9.4),
	}
	if !reflect.DeepEqual(resolved.Metadata, want) {
		gotJSON, _ := json.Marshal(resolved.Metadata)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("metadata mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

// A record without any metadata field reports none, rather than an empty
// struct a consumer would have to recognise as empty.
func TestMangaFireRecordWithoutMetadataReportsNone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece","genres":[],"rating":0,"ratingCount":12}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"meta":{"hasNext":false}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Metadata != nil {
		t.Fatalf("expected no metadata from a bare record, got %+v", resolved.Metadata)
	}
}

// The number fields' guards: a rating outside 0-10 or a year that is not a
// plausible start year is dropped rather than stored, and a string that only
// looks numeric to strconv ("NaN") is not a number.
func TestMangaFireLooseNumberGuards(t *testing.T) {
	cases := []struct {
		raw        string
		wantRating *float64
		wantYear   int
		wantCount  int
	}{
		{raw: `9.4`, wantRating: float64Pointer(9.4)},
		{raw: `"8.45"`, wantRating: float64Pointer(8.45)},
		{raw: `11`, wantCount: 11},
		{raw: `0`},
		{raw: `-3`},
		{raw: `"NaN"`},
		{raw: `"Infinity"`},
		{raw: `null`},
		{raw: `true`},
		{raw: `2022`, wantYear: 2022, wantCount: 2022},
		{raw: `"2022"`, wantYear: 2022, wantCount: 2022},
		{raw: `20240501`, wantCount: 20240501},
		{raw: `2022.5`},
	}

	for _, tc := range cases {
		var number looseNumber
		if err := json.Unmarshal([]byte(tc.raw), &number); err != nil {
			t.Fatalf("%s: decode must never fail, got %v", tc.raw, err)
		}
		if got := number.rating(10); !reflect.DeepEqual(got, tc.wantRating) {
			t.Fatalf("%s: rating = %v, want %v", tc.raw, got, tc.wantRating)
		}
		if got := number.year(); got != tc.wantYear {
			t.Fatalf("%s: year = %d, want %d", tc.raw, got, tc.wantYear)
		}
		if got := number.count(); got != tc.wantCount {
			t.Fatalf("%s: count = %d, want %d", tc.raw, got, tc.wantCount)
		}
	}
}
