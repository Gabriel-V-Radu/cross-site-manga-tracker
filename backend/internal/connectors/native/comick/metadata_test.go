package comick

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func float64Pointer(value float64) *float64 {
	return &value
}

// kagurabachiMetadata is what the kagurabachiHandler comic response describes.
func kagurabachiMetadata() *connectors.SeriesMetadata {
	return &connectors.SeriesMetadata{
		Type:          connectors.SeriesTypeManga,
		Status:        connectors.SeriesStatusOngoing,
		Year:          2023,
		Genres:        []string{"Action", "Comedy"},
		Tags:          []string{"Supernatural", "Ninja"},
		Demographic:   "Shounen",
		ContentRating: connectors.ContentRatingSafe,
		Description:   "Young Chihiro spends his days training under his famous swordsmith father. The goofy father and the serious son--they thought these days would last forever.\n\nBut suddenly, tragedy strikes.",
		Authors:       []string{"Hokazono Takeru"},
		Artists:       []string{"Hokazono Takeru"},
		Rating:        float64Pointer(8.54),
		RatingVotes:   1733,
	}
}

// countComicHits wraps a handler, counting the requests that reach the comic
// endpoint for slug.
func countComicHits(handler http.Handler, slug string, hits *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/comic/"+slug {
			*hits++
		}
		handler.ServeHTTP(w, r)
	})
}

func assertMetadata(t *testing.T, got *connectors.SeriesMetadata, want *connectors.SeriesMetadata) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("metadata mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

// The comic response the resolve already downloads describes the series: the
// type from the country, the numeric status and demographic decoded, tags split
// by group (genres and themes kept, format and content dropped), the people
// from beside the comic object and the bayesian score as the rating.
func TestResolveByURLReadsSeriesMetadata(t *testing.T) {
	connector := newTestConnector(t, kagurabachiHandler(t))

	result, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	assertMetadata(t, result.Metadata, kagurabachiMetadata())
}

// The metadata is cached with the rest of the comic record, so repeat resolves
// keep reporting it without a second comic request — and a caller editing one
// result cannot reach into what the next resolve reads.
func TestResolveByURLKeepsMetadataAcrossCachedResolves(t *testing.T) {
	hits := 0
	connector := newTestConnector(t, countComicHits(kagurabachiHandler(t), "kagura-bachi", &hits))

	first, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	first.Metadata.Genres[0] = "Edited"
	*first.Metadata.Rating = 1

	second, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected one comic request across repeat resolves, got %d", hits)
	}
	assertMetadata(t, second.Metadata, kagurabachiMetadata())
}

// A record seeded from search results carries no metadata — the search row is
// slimmer — and a resolve served from it reports none rather than spending the
// comic request the seeding exists to save.
func TestSearchSeededRecordReportsNoMetadata(t *testing.T) {
	hits := 0
	connector := newTestConnector(t, countComicHits(kagurabachiHandler(t), "kagura-bachi", &hits))

	results, err := connector.SearchByTitle(context.Background(), "kagurabachi", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	for _, result := range results {
		if result.Metadata != nil {
			t.Fatalf("search results must not carry metadata, got %+v", result.Metadata)
		}
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if hits != 0 {
		t.Fatalf("expected the seeded record to spare the comic request, got %d", hits)
	}
	if resolved.Metadata != nil {
		t.Fatalf("expected no metadata from a search-seeded record, got %+v", resolved.Metadata)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 128 {
		t.Fatalf("expected the chapter to resolve as usual, got %v", resolved.LatestChapter)
	}
}

// A search covering a slug that was just resolved must not replace the full
// record with its slimmer row: the link scan searches tracker titles across
// sites, and every resolve until the TTL lapsed would lose its metadata.
func TestSearchKeepsTheFullCachedRecord(t *testing.T) {
	hits := 0
	connector := newTestConnector(t, countComicHits(kagurabachiHandler(t), "kagura-bachi", &hits))

	if _, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi"); err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	if _, err := connector.SearchByTitle(context.Background(), "kagurabachi", 5); err != nil {
		t.Fatalf("search failed: %v", err)
	}
	resolved, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected one comic request, got %d", hits)
	}
	assertMetadata(t, resolved.Metadata, kagurabachiMetadata())
}

// A metadata field that changes shape costs that field alone. The comic record
// supplies the hid the chapter request needs, so a status sent as a string, a
// people list sent as an object or a malformed tag must never fail the resolve,
// and the fields that still read correctly are kept.
func TestMalformedMetadataNeverFailsResolve(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/comic/odd", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comic":{"hid":"oddHID","slug":"odd","title":"Odd",
			"country":7,"status":"2","year":"2019","demographic":"3","content_rating":{"x":1},
			"bayesian_rating":8.45,"rating_count":"lots","desc":null,
			"md_comic_md_genres":[42,{"md_genres":null},{"md_genres":{"name":"Drama","group":"genre"}},{"md_genres":{"name":["x"],"group":"Theme"}}]},
			"authors":{"name":"Not A List"},"artists":["Plain Artist",{"slug":"no-name"},7],"demographic":null}`))
	})
	mux.HandleFunc("/comic/oddHID/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chapters":[{"hid":"x","chap":"12","lang":"en"}]}`))
	})
	connector := newTestConnector(t, mux)

	result, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/odd")
	if err != nil {
		t.Fatalf("malformed metadata must not fail the resolve: %v", err)
	}
	if result.Title != "Odd" || result.LatestChapter == nil || *result.LatestChapter != 12 {
		t.Fatalf("expected the title and chapter to resolve as usual, got %q chapter %v", result.Title, result.LatestChapter)
	}
	assertMetadata(t, result.Metadata, &connectors.SeriesMetadata{
		Status:      connectors.SeriesStatusCompleted,
		Year:        2019,
		Genres:      []string{"Drama"},
		Demographic: "Seinen",
		Artists:     []string{"Plain Artist"},
		Rating:      float64Pointer(8.45),
	})
}

// A comic response with no metadata field reports none, rather than an empty
// struct a consumer would have to recognise as empty.
func TestResolveByURLWithoutMetadataReportsNone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/comic/bare", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comic":{"hid":"bareHID","slug":"bare","title":"Bare","bayesian_rating":null,"rating_count":0,"md_comic_md_genres":[]},"authors":[],"artists":[]}`))
	})
	mux.HandleFunc("/comic/bareHID/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chapters":[]}`))
	})
	connector := newTestConnector(t, mux)

	result, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/bare")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.Metadata != nil {
		t.Fatalf("expected no metadata from a bare record, got %+v", result.Metadata)
	}
}

// The coded fields: the country picks the type, the numeric status and the
// fallback demographic decode to their labels (unknown codes to nothing), and
// bayesian_rating reads as a string or a number, an unrated "0.00" as none.
func TestComicMetadataCodes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *connectors.SeriesMetadata
	}{
		{
			name: "korean on hiatus with a string score",
			body: `{"comic":{"country":"kr","status":4,"demographic":3,"bayesian_rating":"9.39","rating_count":11358},"demographic":"Seinen"}`,
			want: &connectors.SeriesMetadata{Type: connectors.SeriesTypeManhwa, Status: connectors.SeriesStatusHiatus, Demographic: "Seinen", Rating: float64Pointer(9.39), RatingVotes: 11358},
		},
		{
			name: "chinese cancelled with a numeric score and only the coded demographic",
			body: `{"comic":{"country":"cn","status":3,"demographic":2,"bayesian_rating":7.1,"rating_count":40}}`,
			want: &connectors.SeriesMetadata{Type: connectors.SeriesTypeManhua, Status: connectors.SeriesStatusCancelled, Demographic: "Shoujo", Rating: float64Pointer(7.1), RatingVotes: 40},
		},
		{
			name: "unknown codes and an unrated score",
			body: `{"comic":{"country":"xx","status":9,"demographic":0,"bayesian_rating":"0.00","rating_count":0,"content_rating":"erotica"}}`,
			want: &connectors.SeriesMetadata{ContentRating: connectors.ContentRatingErotica},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var response apiComicResponse
			if err := json.Unmarshal([]byte(tc.body), &response); err != nil {
				t.Fatalf("decode: %v", err)
			}
			assertMetadata(t, response.metadata(), tc.want)
		})
	}
}
