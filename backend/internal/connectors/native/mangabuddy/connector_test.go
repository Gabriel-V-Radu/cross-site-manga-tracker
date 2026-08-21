package mangabuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestConnector(t *testing.T, handler http.Handler) (*Connector, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	connector := NewConnectorWithOptions(server.URL, []string{"mangabuddy1.co.uk"}, &http.Client{Timeout: 5 * time.Second})
	return connector, server
}

// seriesHandler serves the /api/series payload for one slug hash.
func seriesHandler(t *testing.T, slugHash string, payload apiSeriesResponse) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/series/"+slugHash, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	return mux
}

func TestResolveByURLReadsLatestChapterAndCover(t *testing.T) {
	const slugHash = "one-piece.ZDMg-w"
	payload := apiSeriesResponse{
		Comic: apiComic{Title: "One Piece", Slug: "one-piece", Cover: "https://cdn.example/one-piece.webp"},
		// Deliberately oldest-first, the order the real API uses.
		Chapters: []apiChapter{
			{Number: 1, Name: "Chapter 1", URL: "https://mangabuddy1.co.uk/series/one-piece.ZDMg-w/chapter-1"},
			{Number: 1187, Name: "Chapter 1187"},
			{Number: 1188, Name: "Chapter 1188"},
		},
	}

	connector, server := newTestConnector(t, seriesHandler(t, slugHash, payload))

	result, err := connector.ResolveByURL(context.Background(), "https://mangabuddy1.co.uk/series/"+slugHash)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.Title != "One Piece" {
		t.Fatalf("expected title, got %q", result.Title)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 1188 {
		t.Fatalf("expected latest chapter 1188, got %#v", result.LatestChapter)
	}
	if result.CoverImageURL != "https://cdn.example/one-piece.webp" {
		t.Fatalf("expected cover, got %q", result.CoverImageURL)
	}
	if result.SourceItemID != slugHash {
		t.Fatalf("expected source item id %q, got %q", slugHash, result.SourceItemID)
	}
	if result.URL != server.URL+"/series/"+slugHash {
		t.Fatalf("unexpected canonical url %q", result.URL)
	}
}

// TestResolveByURLIgnoresCorruptChapterNumbers covers a real payload defect: one
// series reports a chapter numbered 13521, which must not become the tracker's
// latest chapter.
func TestResolveByURLIgnoresCorruptChapterNumbers(t *testing.T) {
	const slugHash = "everythings-coming-up-roses.ZDABCD"
	payload := apiSeriesResponse{
		Comic: apiComic{Title: "Everything's Coming Up Roses", Slug: "everythings-coming-up-roses"},
		Chapters: []apiChapter{
			{Number: 160},
			{Number: 13521},
			{Number: 162},
		},
	}

	connector, _ := newTestConnector(t, seriesHandler(t, slugHash, payload))

	result, err := connector.ResolveByURL(context.Background(), "https://mangabuddy1.co.uk/series/"+slugHash)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 162 {
		t.Fatalf("expected the corrupt 13521 to be ignored and 162 kept, got %#v", result.LatestChapter)
	}
}

// TestResolveByURLDropsIsolatedOutlierChapters covers the junk entries the API
// plants inside the hard cap: a lone "Chapter 1000" on a series whose real run
// ends at 249, and a 9999 stacked on a 999 above a real 209. Both were observed
// live and had poisoned trackers' latest-chapter counts.
func TestResolveByURLDropsIsolatedOutlierChapters(t *testing.T) {
	cases := []struct {
		name     string
		chapters []apiChapter
		want     float64
	}{
		{
			name:     "single junk entry far above the real run",
			chapters: []apiChapter{{Number: 248}, {Number: 249}, {Number: 1000}},
			want:     249,
		},
		{
			name:     "stacked junk entries are peeled one by one",
			chapters: []apiChapter{{Number: 208}, {Number: 209}, {Number: 999}, {Number: 9999}},
			want:     209,
		},
		{
			name:     "exactly the cap is junk too",
			chapters: []apiChapter{{Number: 61}, {Number: 62}, {Number: 10000}},
			want:     62,
		},
		{
			name:     "split chapters and ordinary gaps survive",
			chapters: []apiChapter{{Number: 249}, {Number: 249.5}, {Number: 260}},
			want:     260,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const slugHash = "outlier-series.ZDTEST"
			payload := apiSeriesResponse{
				Comic:    apiComic{Title: "Outlier Series", Slug: "outlier-series"},
				Chapters: tc.chapters,
			}
			connector, _ := newTestConnector(t, seriesHandler(t, slugHash, payload))

			result, err := connector.ResolveByURL(context.Background(), "https://mangabuddy1.co.uk/series/"+slugHash)
			if err != nil {
				t.Fatalf("resolve failed: %v", err)
			}
			if result.LatestChapter == nil || *result.LatestChapter != tc.want {
				t.Fatalf("expected latest chapter %v, got %#v", tc.want, result.LatestChapter)
			}
		})
	}
}

// TestResolveByURLPublishesNoReleaseDate pins the decision not to trust the
// API's per-chapter "time" field, which misreports ages badly enough to churn
// stored release dates.
func TestResolveByURLPublishesNoReleaseDate(t *testing.T) {
	const slugHash = "some-series.ZDXYZ1"
	payload := apiSeriesResponse{
		Comic:    apiComic{Title: "Some Series", Slug: "some-series"},
		Chapters: []apiChapter{{Number: 5}},
	}

	connector, _ := newTestConnector(t, seriesHandler(t, slugHash, payload))

	result, err := connector.ResolveByURL(context.Background(), "https://mangabuddy1.co.uk/series/"+slugHash)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LastUpdatedAt != nil {
		t.Fatalf("expected no release date, got %v", result.LastUpdatedAt)
	}
}

func TestResolveChapterURLPrefersPayloadURL(t *testing.T) {
	const slugHash = "one-piece.ZDMg-w"
	payload := apiSeriesResponse{
		Comic: apiComic{Title: "One Piece", Slug: "one-piece"},
		Chapters: []apiChapter{
			{Number: 1187, URL: "https://mangabuddy1.co.uk/series/one-piece.ZDMg-w/chapter-1187"},
			{Number: 1188.5},
		},
	}

	connector, server := newTestConnector(t, seriesHandler(t, slugHash, payload))
	sourceURL := "https://mangabuddy1.co.uk/series/" + slugHash

	got, err := connector.ResolveChapterURL(context.Background(), sourceURL, 1187)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if want := "https://mangabuddy1.co.uk/series/one-piece.ZDMg-w/chapter-1187"; got != want {
		t.Fatalf("expected payload url\n got %s\nwant %s", got, want)
	}

	// A decimal chapter without a URL falls back to a derived path that keeps the
	// fractional part intact.
	got, err = connector.ResolveChapterURL(context.Background(), sourceURL, 1188.5)
	if err != nil {
		t.Fatalf("resolve decimal chapter failed: %v", err)
	}
	if want := server.URL + "/series/" + slugHash + "/chapter-1188.5"; got != want {
		t.Fatalf("expected derived url\n got %s\nwant %s", got, want)
	}

	if _, err := connector.ResolveChapterURL(context.Background(), sourceURL, 9999); err == nil {
		t.Fatalf("expected an error for a missing chapter")
	}
}

func TestSearchByTitleMapsResults(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); got != "one piece" {
			t.Errorf("expected search term to be forwarded, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiSearchResponse{Comics: []apiComic{
			{Title: "One Piece", Slug: "one-piece", SlugHash: "one-piece.ZDMg-w", Image: "https://cdn.example/op.webp"},
			{Title: "One Piece Party", Slug: "one-piece-party", SlugHash: "one-piece-party.ZDMRKQ"},
			{Title: "No Identifier", Slug: "no-id"},
		}})
	})

	connector, _ := newTestConnector(t, mux)

	results, err := connector.SearchByTitle(context.Background(), "one piece", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	// The entry without a slug hash is unusable and must be dropped.
	if len(results) != 2 {
		t.Fatalf("expected 2 usable results, got %d", len(results))
	}
	if results[0].SourceItemID != "one-piece.ZDMg-w" {
		t.Fatalf("unexpected source item id %q", results[0].SourceItemID)
	}
	if results[0].CoverImageURL != "https://cdn.example/op.webp" {
		t.Fatalf("expected cover from image field, got %q", results[0].CoverImageURL)
	}
	if results[0].SourceKey != "mangabuddy" {
		t.Fatalf("unexpected source key %q", results[0].SourceKey)
	}
}

func TestSearchByTitleHonoursLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, _ *http.Request) {
		comics := make([]apiComic, 0, 30)
		for index := 0; index < 30; index++ {
			comics = append(comics, apiComic{Title: "T", Slug: "t", SlugHash: "t" + strings.Repeat("x", index) + ".ZD"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiSearchResponse{Comics: comics})
	})

	connector, _ := newTestConnector(t, mux)

	results, err := connector.SearchByTitle(context.Background(), "t", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected the limit to be honoured, got %d", len(results))
	}
}

func TestParseSeriesURL(t *testing.T) {
	connector := NewConnector()

	cases := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{name: "series", rawURL: "https://mangabuddy1.co.uk/series/one-piece.ZDMg-w", want: "one-piece.ZDMg-w"},
		{name: "chapter", rawURL: "https://mangabuddy1.co.uk/series/one-piece.ZDMg-w/chapter-1188", want: "one-piece.ZDMg-w"},
		{name: "trailing slash", rawURL: "https://mangabuddy1.co.uk/series/a-b.ZD1/", want: "a-b.ZD1"},
		{name: "foreign host", rawURL: "https://mangafire.to/title/dkw-one-piece", wantErr: true},
		{name: "wrong path", rawURL: "https://mangabuddy1.co.uk/home", wantErr: true},
		{name: "empty", rawURL: "   ", wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := connector.parseSeriesURL(testCase.rawURL)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", testCase.rawURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

func TestResolveByURLSurfacesMissingSeries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/series/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":""}`))
	})

	connector, _ := newTestConnector(t, mux)

	if _, err := connector.ResolveByURL(context.Background(), "https://mangabuddy1.co.uk/series/nope.ZD0"); err == nil {
		t.Fatalf("expected an error for a missing series")
	}
}

func TestHealthCheckFailsOnBadStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	connector, _ := newTestConnector(t, mux)

	if err := connector.HealthCheck(context.Background()); err == nil {
		t.Fatalf("expected health check to fail")
	}
}
