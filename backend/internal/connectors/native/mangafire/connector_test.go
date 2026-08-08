package mangafire

import (
	"context"
	"fmt"
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
	// Serves every language regardless of the `language` param, mirroring the
	// API's habit of ignoring params it does not recognise: the connector's own
	// filter has to be what keeps other languages out.
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":8000188,"number":1188,"name":"raw","language":"ja","type":"official","createdAt":1783300000},
			{"id":7511775,"number":1187,"name":"The Cause","language":"en","type":"unofficial","createdAt":1783047602},
			{"id":7452440,"number":1186,"name":"Encore une fois","language":"fr","type":"unofficial","createdAt":1782659714},
			{"id":7462702,"number":1186,"name":"One More Time","language":"en","type":"official","createdAt":1782777604}
		]}`))
	})

	return httptest.NewServer(mux)
}

// The Japanese raws lead the English release, which is the shape that made
// MangaFire report an unreadable chapter number (Reiwa no Dara-san: 57.5 in
// Japanese against 54 in English). The title payload advertises the Japanese
// number and the English listing the real one; the connector must report the
// latter, and date it from the English upload rather than the raw's.
func TestMangaFireReportsEnglishChapterNotSiteWideLatest(t *testing.T) {
	server := newFakeAPIServer(t)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil {
		t.Fatalf("expected a latest chapter")
	}
	if *resolved.LatestChapter == 1188 {
		t.Fatalf("reported the Japanese chapter 1188 instead of the English one")
	}
	if *resolved.LatestChapter != 1187 {
		t.Fatalf("expected latest English chapter 1187, got %v", *resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || !resolved.LastUpdatedAt.Equal(time.Unix(1783047602, 0).UTC()) {
		t.Fatalf("expected the English upload date, got %v", resolved.LastUpdatedAt)
	}
}

// A title MangaFire carries only in other languages has no chapter this reader
// can open, so the connector reports none rather than the foreign number — the
// poller then leaves the tracker's stored progress alone.
func TestMangaFireReportsNoChapterWhenNoEnglishRelease(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/jpn", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":9,"hid":"jpn","slug":"raws-only","title":"Raws Only","latestChapter":57.5,"chapterUpdatedAt":"6mos ago"}}`))
	})
	mux.HandleFunc("/api/titles/jpn/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":1,"number":57.5,"language":"ja","createdAt":1768103689}],"meta":{"hasNext":false}}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/jpn-raws-only")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter != nil {
		t.Fatalf("expected no chapter for a title without an English release, got %v", *resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt != nil {
		t.Fatalf("expected no release date without an English release, got %v", resolved.LastUpdatedAt)
	}
	if resolved.Title != "Raws Only" {
		t.Fatalf("expected the title to still resolve, got %q", resolved.Title)
	}
}

// The chapter listing is now what the reported number rests on, so a failure to
// read it has to surface as a failed poll rather than as "no chapters" — which
// the poller would take at face value.
func TestMangaFireChapterFetchFailureIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece","latestChapter":1187,"chapterUpdatedAt":"2d ago"}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	if _, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece"); err == nil {
		t.Fatalf("expected an unreadable chapter listing to fail the resolve")
	}
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
		// The search payload's chapter number and date span every language and
		// the endpoint cannot be narrowed to one, so search results carry
		// neither; the form the user submits is filled from ResolveByURL, which
		// reads the English listing.
		if item.LatestChapter != nil {
			t.Fatalf("expected no cross-language chapter number in search results for %s, got %v", item.SourceItemID, *item.LatestChapter)
		}
		if item.LastUpdatedAt != nil {
			t.Fatalf("expected no cross-language release date in search results for %s, got %v", item.SourceItemID, item.LastUpdatedAt)
		}

		switch item.SourceItemID {
		case "dkw-one-piece":
			if item.CoverImageURL != "https://cdn.example/op.jpg" {
				t.Fatalf("unexpected One Piece cover: %s", item.CoverImageURL)
			}
		case "oo4-one-punch-man":
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

// The chapter listing used to be fetched only when the title payload's
// latestChapter changed. That signal cannot see an English release on a title
// whose Japanese raws are further ahead — the site-wide number stays put, and so
// did the cached English one, which is how tracked titles went quiet. Every
// resolve must go back to the listing.
func TestMangaFireRereadsChapterListingOnEveryResolve(t *testing.T) {
	chapterRequests := 0
	latestEnglish := 1187

	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Frozen: the Japanese raws lead, so neither field moves when English
		// gains a chapter.
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece","latestChapter":1200,"chapterUpdatedAt":"6mos ago"}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		chapterRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[{"id":75117,"number":%d,"language":"en","createdAt":%d}],"meta":{"hasNext":false}}`,
			latestEnglish, 1783047602+latestEnglish)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	first, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	if first.LatestChapter == nil || *first.LatestChapter != 1187 {
		t.Fatalf("expected latest English chapter 1187, got %v", first.LatestChapter)
	}

	latestEnglish = 1188

	second, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if second.LatestChapter == nil || *second.LatestChapter != 1188 {
		t.Fatalf("new English chapter went unnoticed: got %v, want 1188", second.LatestChapter)
	}
	if second.LastUpdatedAt == nil || !second.LastUpdatedAt.Equal(time.Unix(1783047602+1188, 0).UTC()) {
		t.Fatalf("expected the new chapter's release date, got %v", second.LastUpdatedAt)
	}
	if chapterRequests != 2 {
		t.Fatalf("expected the listing to be re-read on each resolve, got %d requests", chapterRequests)
	}
}

func TestMangaFireConnectorCoolsDownAfterForbidden(t *testing.T) {
	requests := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Access denied"))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	if err := connector.HealthCheck(context.Background()); err == nil {
		t.Fatalf("expected error on forbidden response")
	}
	if requests != 1 {
		t.Fatalf("expected a single request before cooldown, got %d", requests)
	}

	if _, err := connector.SearchByTitle(context.Background(), "one piece", 5); err == nil {
		t.Fatalf("expected fail-fast error while cooling down")
	}
	if requests != 1 {
		t.Fatalf("expected no additional requests while cooling down, got %d", requests)
	}
}
