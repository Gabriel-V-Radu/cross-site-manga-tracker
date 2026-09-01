package mangaupdates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 114563652 is "01w7hvo" in zero-padded base36 — the real Nano Machine id.
const testSeriesID = int64(114563652)

func newTestServer(t *testing.T, releaseDate string, releases bool) (*httptest.Server, *Connector) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/series/%d", testSeriesID), func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{
			"series_id": 114563652,
			"title": "Nano Machine",
			"url": "https://www.mangaupdates.com/series/01w7hvo/nano-machine",
			"image": {"url": {"original": "https://cdn.mangaupdates.com/image/i491625.jpg"}},
			"latest_chapter": 325,
			"associated": [{"title": "Nano Mashin"}, {"title": "나노마신"}]
		}`))
	})
	mux.HandleFunc("/releases/search", func(w http.ResponseWriter, r *http.Request) {
		if !releases {
			w.Write([]byte(`{"results": []}`))
			return
		}
		var payload struct {
			Search     string `json:"search"`
			SearchType string `json:"search_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.SearchType != "series" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		older := parseReleaseDate(releaseDate).AddDate(0, 0, -7).Format("2006-01-02")
		fmt.Fprintf(w, `{"results": [
			{"record": {"chapter": "325", "release_date": "%s"}},
			{"record": {"chapter": "323-324", "release_date": "%s"}},
			{"record": {"chapter": "Oneshot", "release_date": "%s"}}
		]}`, releaseDate, older, older)
	})
	mux.HandleFunc("/series/search", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"results": [
			{"record": {"series_id": 114563652, "title": "Nano Machine",
				"url": "https://www.mangaupdates.com/series/01w7hvo/nano-machine",
				"image": {"url": {"original": "https://cdn.mangaupdates.com/image/i491625.jpg"}}}},
			{"record": {"series_id": 999, "title": "Something Else",
				"url": "https://www.mangaupdates.com/series/0000rr/something-else"}}
		]}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	connector := NewConnectorWithOptions(server.URL, []string{"mangaupdates.com"}, &http.Client{Timeout: 5 * time.Second})
	return server, connector
}

func recentDate() string {
	return time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")
}

func TestResolveByURLDecodesBase36AndReadsReleases(t *testing.T) {
	_, connector := newTestServer(t, recentDate(), true)

	result, err := connector.ResolveByURL(context.Background(), "https://www.mangaupdates.com/series/01w7hvo/nano-machine")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if result.Title != "Nano Machine" {
		t.Fatalf("title = %q", result.Title)
	}
	if result.SourceItemID != "114563652" {
		t.Fatalf("source item id = %q", result.SourceItemID)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 325 {
		t.Fatalf("latest chapter = %v, want 325", result.LatestChapter)
	}
	if result.LastUpdatedAt == nil {
		t.Fatal("expected a release date")
	}
	// The non-Latin associated title is filtered out.
	if len(result.RelatedTitles) != 1 || result.RelatedTitles[0] != "Nano Mashin" {
		t.Fatalf("related titles = %v", result.RelatedTitles)
	}
	if !strings.Contains(result.URL, "/series/01w7hvo/") {
		t.Fatalf("url = %q", result.URL)
	}
}

// TestStaleFeedReportsNoChapter pins the licensed-series guard: a feed whose
// newest release is months old must report nothing rather than a low number
// that would eventually walk the tracker backwards.
func TestStaleFeedReportsNoChapter(t *testing.T) {
	_, connector := newTestServer(t, "2023-04-25", true)

	result, err := connector.ResolveByURL(context.Background(), "https://www.mangaupdates.com/series/01w7hvo/nano-machine")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.LatestChapter != nil {
		t.Fatalf("stale feed reported chapter %v, want none", *result.LatestChapter)
	}
	if result.LastUpdatedAt != nil {
		t.Fatalf("stale feed reported a date, want none")
	}
	// The series is still resolvable for linking purposes.
	if result.Title != "Nano Machine" {
		t.Fatalf("title = %q", result.Title)
	}
}

func TestEmptyFeedReportsNoChapter(t *testing.T) {
	_, connector := newTestServer(t, "", false)

	result, err := connector.ResolveByURL(context.Background(), "https://www.mangaupdates.com/series/01w7hvo/nano-machine")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.LatestChapter != nil {
		t.Fatalf("empty feed reported chapter %v, want none", *result.LatestChapter)
	}
}

// A release search that fails must fail the resolve rather than pass as "no
// releases": the poller treats a result without a chapter as a successful
// check, so a transient 429 here used to skip the fallback sources silently.
func TestReleaseSearchFailureIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/series/%d", testSeriesID), func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"series_id": 114563652, "title": "Nano Machine", "latest_chapter": 325}`))
	})
	mux.HandleFunc("/releases/search", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	connector := NewConnectorWithOptions(server.URL, []string{"mangaupdates.com"}, &http.Client{Timeout: 5 * time.Second})

	result, err := connector.ResolveByURL(context.Background(), "https://www.mangaupdates.com/series/01w7hvo/nano-machine")
	if err == nil {
		t.Fatalf("expected the release search failure to fail the resolve, got %+v", result)
	}
}

func TestResolveByURLRejectsForeignHost(t *testing.T) {
	_, connector := newTestServer(t, recentDate(), true)

	if _, err := connector.ResolveByURL(context.Background(), "https://example.com/series/01w7hvo"); err == nil {
		t.Fatal("expected foreign host to be rejected")
	}
}

func TestSearchByTitleFiltersToQuery(t *testing.T) {
	_, connector := newTestServer(t, recentDate(), true)

	results, err := connector.SearchByTitle(context.Background(), "Nano Machine", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Nano Machine" {
		t.Fatalf("results = %+v, want only Nano Machine", results)
	}
	if results[0].SourceItemID != "114563652" {
		t.Fatalf("source item id = %q", results[0].SourceItemID)
	}
}

func TestParseChapterString(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
		none bool
	}{
		{raw: "325", want: 325},
		{raw: "23-24", want: 24},
		{raw: "17.2", want: 17.2},
		{raw: "Oneshot", none: true},
		{raw: "13521", none: true},
	}
	for _, tc := range cases {
		got := parseChapterString(tc.raw)
		if tc.none {
			if got != nil {
				t.Fatalf("parse %q = %v, want none", tc.raw, *got)
			}
			continue
		}
		if got == nil || *got != tc.want {
			t.Fatalf("parse %q = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
