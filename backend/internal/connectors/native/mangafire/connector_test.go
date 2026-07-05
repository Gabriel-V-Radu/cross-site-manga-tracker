package mangafire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newFakeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		keyword := r.URL.Query().Get("keyword")
		switch keyword {
		case "", "one":
			_, _ = w.Write([]byte(`{"items":[
				{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece","poster":{"small":"https://cdn.example/op@100.jpg","medium":"https://cdn.example/op@280.jpg","large":"https://cdn.example/op.jpg"},"latestChapter":1187,"chapterUpdatedAt":"2d ago","url":"/title/dkw-one-piece"},
				{"id":2,"hid":"oo4","slug":"one-punch-man","title":"One-Punch Man","poster":{"medium":"https://cdn.example/opm@280.jpg"},"latestChapter":264,"chapterUpdatedAt":"1mo ago","url":"/title/oo4-one-punch-man"}
			],"meta":{"total":2}}`))
		default:
			_, _ = w.Write([]byte(`{"items":[],"meta":{"total":0}}`))
		}
	})
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece","poster":{"small":"https://cdn.example/op@100.jpg","medium":"https://cdn.example/op@280.jpg","large":"https://cdn.example/op.jpg"},"latestChapter":1187,"chapterUpdatedAt":"2d ago","url":"/title/dkw-one-piece","altTitles":["ワンピース","One Piece. Большой куш","Pirate Legacy"]}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":7511775,"number":1187,"name":"The Cause","language":"en","type":"unofficial","createdAt":1783047602},
			{"id":7452440,"number":1186,"name":"Encore une fois","language":"fr","type":"unofficial","createdAt":1782659714},
			{"id":7462702,"number":1186,"name":"One More Time","language":"en","type":"official","createdAt":1782777604}
		]}`))
	})

	return httptest.NewServer(mux)
}

func TestMangaFireConnector(t *testing.T) {
	server := newFakeAPIServer(t)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	if err := connector.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "dkw-one-piece" {
		t.Fatalf("expected source item id dkw-one-piece, got %s", resolved.SourceItemID)
	}
	if resolved.Title != "One Piece" {
		t.Fatalf("expected title One Piece, got %s", resolved.Title)
	}
	if resolved.URL != "https://mangafire.to/title/dkw-one-piece" {
		t.Fatalf("unexpected canonical url: %s", resolved.URL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 1187 {
		t.Fatalf("expected latest chapter 1187, got %v", resolved.LatestChapter)
	}
	if resolved.CoverImageURL != "https://cdn.example/op.jpg" {
		t.Fatalf("unexpected cover image: %s", resolved.CoverImageURL)
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected release date from chapters endpoint")
	}
	expectedReleaseAt := time.Unix(1783047602, 0).UTC()
	if !resolved.LastUpdatedAt.Equal(expectedReleaseAt) {
		t.Fatalf("expected release date %s, got %s", expectedReleaseAt.Format(time.RFC3339), resolved.LastUpdatedAt.Format(time.RFC3339))
	}

	foundAlias := false
	for _, related := range resolved.RelatedTitles {
		if related == resolved.Title {
			t.Fatalf("did not expect primary title in related titles: %q", related)
		}
		if related == "Pirate Legacy" {
			foundAlias = true
		}
		if related == "ワンピース" {
			t.Fatalf("did not expect non-English alt title in related titles")
		}
	}
	if !foundAlias {
		t.Fatalf("expected related titles to include English alias, got %v", resolved.RelatedTitles)
	}
}

func TestMangaFireConnectorResolvesLegacyMangaURL(t *testing.T) {
	server := newFakeAPIServer(t)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/manga/one-piecee.dkw")
	if err != nil {
		t.Fatalf("resolve legacy url failed: %v", err)
	}
	if resolved.SourceItemID != "dkw-one-piece" {
		t.Fatalf("expected canonical source item id dkw-one-piece, got %s", resolved.SourceItemID)
	}
	if resolved.URL != "https://mangafire.to/title/dkw-one-piece" {
		t.Fatalf("expected legacy url to migrate to /title form, got %s", resolved.URL)
	}
}

func TestMangaFireConnectorSearchByTitle(t *testing.T) {
	server := newFakeAPIServer(t)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	results, err := connector.SearchByTitle(context.Background(), "one", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, item := range results {
		switch item.SourceItemID {
		case "dkw-one-piece":
			if item.LatestChapter == nil || *item.LatestChapter != 1187 {
				t.Fatalf("expected One Piece latest chapter 1187, got %v", item.LatestChapter)
			}
			if item.LastUpdatedAt == nil {
				t.Fatalf("expected One Piece release date from relative time")
			}
			if item.CoverImageURL != "https://cdn.example/op.jpg" {
				t.Fatalf("unexpected One Piece cover: %s", item.CoverImageURL)
			}
		case "oo4-one-punch-man":
			if item.LatestChapter == nil || *item.LatestChapter != 264 {
				t.Fatalf("expected One-Punch Man latest chapter 264, got %v", item.LatestChapter)
			}
			if item.CoverImageURL != "https://cdn.example/opm@280.jpg" {
				t.Fatalf("expected medium poster fallback, got %s", item.CoverImageURL)
			}
		default:
			t.Fatalf("unexpected search source id: %s", item.SourceItemID)
		}
	}
}

func TestMangaFireConnectorResolveChapterURL(t *testing.T) {
	server := newFakeAPIServer(t)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	for _, sourceURL := range []string{
		"https://mangafire.to/title/dkw-one-piece",
		"https://mangafire.to/manga/one-piecee.dkw",
	} {
		chapterURL, err := connector.ResolveChapterURL(context.Background(), sourceURL, 1186)
		if err != nil {
			t.Fatalf("resolve chapter url failed for %s: %v", sourceURL, err)
		}
		if chapterURL != "https://mangafire.to/title/dkw-one-piece/7462702" {
			t.Fatalf("expected english chapter entry to win for %s, got %s", sourceURL, chapterURL)
		}
	}

	if _, err := connector.ResolveChapterURL(context.Background(), "https://mangafire.to/title/dkw-one-piece", 9999); err == nil {
		t.Fatalf("expected error for unknown chapter")
	}
}

func TestParseTitleURL(t *testing.T) {
	connector := NewConnector()

	cases := []struct {
		rawURL   string
		wantHID  string
		wantSlug string
		wantErr  bool
	}{
		{rawURL: "https://mangafire.to/title/dkw-one-piece", wantHID: "dkw", wantSlug: "one-piece"},
		{rawURL: "https://mangafire.to/title/dkw", wantHID: "dkw", wantSlug: ""},
		{rawURL: "https://mangafire.to/manga/one-piecee.dkw", wantHID: "dkw", wantSlug: ""},
		{rawURL: "https://mangafire.to/read/one-piecee.dkw/en/chapter-1", wantHID: "dkw", wantSlug: ""},
		{rawURL: "https://example.com/title/dkw-one-piece", wantErr: true},
		{rawURL: "https://mangafire.to/genre/action", wantErr: true},
		{rawURL: "https://mangafire.to/manga/no-legacy-id", wantErr: true},
	}

	for _, testCase := range cases {
		hid, slug, err := connector.parseTitleURL(testCase.rawURL)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("expected error for %s", testCase.rawURL)
			}
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", testCase.rawURL, err)
		}
		if hid != testCase.wantHID || slug != testCase.wantSlug {
			t.Fatalf("parse %s: expected (%s, %s), got (%s, %s)", testCase.rawURL, testCase.wantHID, testCase.wantSlug, hid, slug)
		}
	}
}

func TestParseRelativeUpdatedAt(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		raw  string
		want *time.Time
	}{
		{raw: "just now", want: timePtr(now)},
		{raw: "30m ago", want: timePtr(now.Add(-30 * time.Minute))},
		{raw: "5h ago", want: timePtr(now.Add(-5 * time.Hour))},
		{raw: "2d ago", want: timePtr(now.AddDate(0, 0, -2))},
		{raw: "3w ago", want: timePtr(now.AddDate(0, 0, -21))},
		{raw: "1mo ago", want: timePtr(now.AddDate(0, -1, 0))},
		{raw: "1yr ago", want: timePtr(now.AddDate(-1, 0, 0))},
		{raw: "", want: nil},
		{raw: "unknown", want: nil},
	}

	for _, testCase := range cases {
		got := parseRelativeUpdatedAt(testCase.raw, now)
		if testCase.want == nil {
			if got != nil {
				t.Fatalf("parse %q: expected nil, got %s", testCase.raw, got.Format(time.RFC3339))
			}
			continue
		}
		if got == nil || !got.Equal(*testCase.want) {
			t.Fatalf("parse %q: expected %s, got %v", testCase.raw, testCase.want.Format(time.RFC3339), got)
		}
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}
