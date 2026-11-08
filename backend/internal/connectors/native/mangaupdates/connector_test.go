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

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
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
			"associated": [{"title": "Nano Mashin"}, {"title": "나노마신"}],
			"description": "Cheon Yeo-Woon, an orphan of the Demonic Cult, is visited by a descendant from the future.<br />\n<br />\nThe [b]nano machine[/b] in his body changes everything.<br />\n(Source: Webtoon)",
			"type": "Manhwa",
			"year": "2020",
			"bayesian_rating": 8.17,
			"rating_votes": 1234,
			"genres": [{"genre": "Action"}, {"genre": "Drama"}, {"genre": "Martial Arts"}, {"genre": "Sci-fi"}],
			"categories": [
				{"series_id": 114563652, "category": "Murim", "votes": 12, "votes_plus": 12, "votes_minus": 0},
				{"series_id": 114563652, "category": "Overpowered Male Lead", "votes": 30, "votes_plus": 31, "votes_minus": 1},
				{"series_id": 114563652, "category": "Unvoted Joke", "votes": 0, "votes_plus": 0, "votes_minus": 0},
				{"series_id": 114563652, "category": "Demonic Cult", "votes": 12, "votes_plus": 12, "votes_minus": 0},
				{"series_id": 114563652, "category": "Downvoted Spoiler", "votes": -3, "votes_plus": 1, "votes_minus": 4}
			],
			"status": "325 Chapters (Ongoing)\n\nS1: 168 Chapters (Complete)",
			"licensed": false,
			"completed": false,
			"authors": [
				{"name": "Han Joong-Wueol-Ya", "author_id": 1, "type": "Author"},
				{"name": "Great H", "author_id": 2, "type": "Author"},
				{"name": "Geum Kang-Bul-Gae", "author_id": 3, "type": "Artist"}
			]
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

// The series record the resolve already downloads describes the work too; the
// metadata is read from that one body, with no further request.
func TestResolveByURLReadsSeriesMetadata(t *testing.T) {
	_, connector := newTestServer(t, recentDate(), true)

	result, err := connector.ResolveByURL(context.Background(), "https://www.mangaupdates.com/series/01w7hvo/nano-machine")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	metadata := result.Metadata
	if metadata == nil {
		t.Fatal("expected metadata")
	}
	if metadata.Type != connectors.SeriesTypeManhwa {
		t.Fatalf("type = %q", metadata.Type)
	}
	if metadata.Year != 2020 {
		t.Fatalf("year = %d", metadata.Year)
	}
	// A finished first season is mentioned, but the series is still running.
	if metadata.Status != connectors.SeriesStatusOngoing {
		t.Fatalf("status = %q", metadata.Status)
	}
	if got := strings.Join(metadata.Genres, "|"); got != "Action|Drama|Martial Arts|Sci-fi" {
		t.Fatalf("genres = %v", metadata.Genres)
	}
	// Most votes first, ties in the site's order, unvoted and downvoted ones
	// dropped.
	if got := strings.Join(metadata.Tags, "|"); got != "Overpowered Male Lead|Murim|Demonic Cult" {
		t.Fatalf("tags = %v", metadata.Tags)
	}
	if got := strings.Join(metadata.Authors, "|"); got != "Han Joong-Wueol-Ya|Great H" {
		t.Fatalf("authors = %v", metadata.Authors)
	}
	if got := strings.Join(metadata.Artists, "|"); got != "Geum Kang-Bul-Gae" {
		t.Fatalf("artists = %v", metadata.Artists)
	}
	wantDescription := "Cheon Yeo-Woon, an orphan of the Demonic Cult, is visited by a descendant from the future.\n\nThe nano machine in his body changes everything.\n(Source: Webtoon)"
	if metadata.Description != wantDescription {
		t.Fatalf("description = %q", metadata.Description)
	}
	if metadata.Rating == nil || *metadata.Rating != 8.17 {
		t.Fatalf("rating = %v", metadata.Rating)
	}
	if metadata.RatingVotes != 1234 {
		t.Fatalf("rating votes = %d", metadata.RatingVotes)
	}
}

// The metadata is a bonus on top of what the tracker needs: a record whose
// descriptive fields are missing, null or of an unexpected type must resolve
// exactly as before, keeping whatever could still be read.
func TestResolveByURLSurvivesOddMetadata(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/series/%d", testSeriesID), func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{
			"series_id": 114563652,
			"title": "Nano Machine",
			"latest_chapter": 325,
			"type": "Manhwa",
			"year": 2020,
			"genres": "Action, Drama",
			"categories": null,
			"description": null,
			"status": null,
			"bayesian_rating": "8.17",
			"authors": [{"name": "  ", "type": "Author"}]
		}`))
	})
	mux.HandleFunc("/releases/search", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"results": [{"record": {"chapter": "325", "release_date": "%s"}}]}`, recentDate())
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	connector := NewConnectorWithOptions(server.URL, []string{"mangaupdates.com"}, &http.Client{Timeout: 5 * time.Second})

	result, err := connector.ResolveByURL(context.Background(), "https://www.mangaupdates.com/series/01w7hvo/nano-machine")
	if err != nil {
		t.Fatalf("odd metadata failed the resolve: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 325 {
		t.Fatalf("latest chapter = %v, want 325", result.LatestChapter)
	}
	metadata := result.Metadata
	if metadata == nil {
		t.Fatal("expected the readable fields to survive")
	}
	if metadata.Type != connectors.SeriesTypeManhwa || metadata.Year != 2020 {
		t.Fatalf("type = %q, year = %d", metadata.Type, metadata.Year)
	}
	if metadata.Genres != nil || metadata.Tags != nil || metadata.Authors != nil || metadata.Rating != nil ||
		metadata.Status != "" || metadata.Description != "" {
		t.Fatalf("expected the unreadable fields to stay empty, got %+v", metadata)
	}

	// A record with nothing descriptive at all carries no metadata.
	if got := seriesMetadata([]byte(`{"series_id": 1, "title": "Bare", "year": "", "bayesian_rating": 0}`)); got != nil {
		t.Fatalf("expected no metadata for a bare record, got %+v", got)
	}
}

func TestSeriesStatus(t *testing.T) {
	cases := []struct {
		raw       string
		completed bool
		want      string
	}{
		{raw: "1 Volume (ongoing)", want: connectors.SeriesStatusOngoing},
		{raw: "135 Chapters (Complete)", want: connectors.SeriesStatusCompleted},
		{raw: "10 Volumes (Complete)\n52 Chapters (Complete)", want: connectors.SeriesStatusCompleted},
		{raw: "S1: 80 Chapters (Complete)\nS2: 337 Chapters (Complete)\nS3: 200 Chapters (Ongoing)", want: connectors.SeriesStatusOngoing},
		{raw: "S1: 50 Chapters (Complete)\nS2: 12 Chapters (Hiatus)", want: connectors.SeriesStatusHiatus},
		{raw: "3 Volumes (Discontinued)", want: connectors.SeriesStatusCancelled},
		{raw: "Ongoing", want: connectors.SeriesStatusOngoing},
		{raw: "5 Volumes (2019)", want: ""},
		{raw: "", want: ""},
		// Completely scanlated implies the series has ended.
		{raw: "", completed: true, want: connectors.SeriesStatusCompleted},
		{raw: "40 Chapters (Ongoing)", completed: true, want: connectors.SeriesStatusCompleted},
	}
	for _, tc := range cases {
		if got := seriesStatus(tc.raw, tc.completed); got != tc.want {
			t.Errorf("seriesStatus(%q, %v) = %q, want %q", tc.raw, tc.completed, got, tc.want)
		}
	}
}

func TestParseYear(t *testing.T) {
	cases := map[string]int{
		`"2026"`:      2026,
		`2020`:        2020,
		`"2019-2021"`: 2019,
		`""`:          0,
		`null`:        0,
		`"20261"`:     0,
		`"0001"`:      0,
		`"9999"`:      0,
	}
	for raw, want := range cases {
		if got := parseYear(json.RawMessage(raw)); got != want {
			t.Errorf("parseYear(%s) = %d, want %d", raw, got, want)
		}
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
	// Only a resolve reports metadata; a search row stays slim.
	if results[0].Metadata != nil {
		t.Fatalf("search row carried metadata: %+v", results[0].Metadata)
	}
}

// MangaUpdates often keeps the romanized original as the primary title and
// matches an English query through an associated name, which it reports as
// hit_title. The post-filter has to accept that name, or an English search
// finds nothing; the matched name also rides along as a related title, and the
// caller's limit holds even though the API answers a full page regardless.
func TestSearchByTitleMatchesHitTitle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/series/search", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"results": [
			{"record": {"series_id": 55665, "title": "Shingeki no Kyojin",
				"url": "https://www.mangaupdates.com/series/16ky/shingeki-no-kyojin"},
				"hit_title": "Attack on Titan"},
			{"record": {"series_id": 999, "title": "Something Else",
				"url": "https://www.mangaupdates.com/series/0000rr/something-else"},
				"hit_title": "Something Else"},
			{"record": {"series_id": 77777, "title": "Shingeki no Kyojin dj - Melt",
				"url": "https://www.mangaupdates.com/series/1nzl/melt"},
				"hit_title": "Attack on Titan dj - Melt"}
		]}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	connector := NewConnectorWithOptions(server.URL, []string{"mangaupdates.com"}, &http.Client{Timeout: 5 * time.Second})

	results, err := connector.SearchByTitle(context.Background(), "Attack on Titan", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 || results[0].Title != "Shingeki no Kyojin" || results[1].SourceItemID != "77777" {
		t.Fatalf("results = %+v, want the two series matched through their hit title", results)
	}
	if len(results[0].RelatedTitles) != 1 || results[0].RelatedTitles[0] != "Attack on Titan" {
		t.Fatalf("related titles = %v, want the matched name", results[0].RelatedTitles)
	}

	limited, err := connector.SearchByTitle(context.Background(), "Attack on Titan", 1)
	if err != nil {
		t.Fatalf("search with limit 1: %v", err)
	}
	if len(limited) != 1 || limited[0].SourceItemID != "55665" {
		t.Fatalf("results = %+v, want only the first match", limited)
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
