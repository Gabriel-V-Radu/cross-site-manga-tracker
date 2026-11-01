package mangadex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func TestMangaDexConnector(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/manga/123e4567-e89b-12d3-a456-426614174000", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id": "123e4567-e89b-12d3-a456-426614174000",
				"attributes": map[string]any{
					"title":       map[string]string{"en": "Test Title"},
					"lastChapter": "42",
				},
				"relationships": []map[string]any{
					{
						"type": "cover_art",
						"attributes": map[string]any{
							"fileName": "test-cover.jpg",
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/manga/abc/feed", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"attributes": map[string]any{"chapter": "7"}},
				{"attributes": map[string]any{"chapter": "7.5"}},
				{"attributes": map[string]any{"chapter": "extra"}},
			},
		})
	})
	mux.HandleFunc("/manga", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id": "abc",
					"attributes": map[string]any{
						"title": map[string]string{"en": "Alpha"},
						"altTitles": []map[string]string{
							{"en": "Solo Leveling"},
							{"ja": "\u4ffa\u3060\u3051\u30ec\u30d9\u30eb\u30a2\u30c3\u30d7\u306a\u4ef6"},
						},
					},
				},
				{
					"id": "def",
					"attributes": map[string]any{
						"title":       map[string]string{"en": "Beta"},
						"lastChapter": "13.5",
						"altTitles": []map[string]string{
							{"en": "Another Story"},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})

	if err := connector.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/123e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Title != "Test Title" {
		t.Fatalf("expected title Test Title, got %s", resolved.Title)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 42 {
		t.Fatalf("expected latest chapter 42, got %v", resolved.LatestChapter)
	}
	if resolved.CoverImageURL == "" {
		t.Fatalf("expected cover image url to be populated")
	}
	if len(resolved.RelatedTitles) != 0 {
		t.Fatalf("expected no related titles when only primary title exists, got %v", resolved.RelatedTitles)
	}

	results, err := connector.SearchByTitle(context.Background(), "leveling solo", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].SourceItemID != "abc" {
		t.Fatalf("expected source id abc, got %s", results[0].SourceItemID)
	}
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 7.5 {
		t.Fatalf("expected fallback latest chapter 7.5 for abc, got %v", results[0].LatestChapter)
	}
	contains := func(values []string, expected string) bool {
		for _, value := range values {
			if value == expected {
				return true
			}
		}
		return false
	}
	if !contains(results[0].RelatedTitles, "Solo Leveling") {
		t.Fatalf("expected english alt title in related titles, got %v", results[0].RelatedTitles)
	}
	if contains(results[0].RelatedTitles, "Alpha") {
		t.Fatalf("did not expect primary title in related titles, got %v", results[0].RelatedTitles)
	}

	nonEnglishResults, err := connector.SearchByTitle(context.Background(), "\u4ffa\u3060\u3051", 10)
	if err != nil {
		t.Fatalf("search with non-English query failed: %v", err)
	}
	if len(nonEnglishResults) != 0 {
		t.Fatalf("expected 0 results for non-English alias query, got %d", len(nonEnglishResults))
	}
}

// A feed that cannot be read must fail the resolve. It used to be swallowed:
// the connector answered with the record's own chapter (or none) and no date,
// which the poller records as a successful check — no fallback to the
// tracker's other sources, no warning. MangaFire's equivalent
// (TestMangaFireChapterFetchFailureIsAnError) has always behaved this way.
func TestMangaDexFeedFailureIsAnError(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+id, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":         id,
				"attributes": map[string]any{"title": map[string]string{"en": "Test Title"}, "lastChapter": "42"},
			},
		})
	})
	mux.HandleFunc("/manga/"+id+"/feed", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	result, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+id)
	if err == nil {
		t.Fatalf("expected the feed failure to fail the resolve, got %+v", result)
	}
}

// An empty feed is a legitimate answer (a licensed series whose chapters were
// pulled, a oneshot), distinct from a feed that could not be read: the resolve
// succeeds and the record's own chapter counter stands in.
func TestMangaDexEmptyFeedKeepsRecordChapter(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+id, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":         id,
				"attributes": map[string]any{"title": map[string]string{"en": "Test Title"}, "lastChapter": "42"},
			},
		})
	})
	mux.HandleFunc("/manga/"+id+"/feed", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	result, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+id)
	if err != nil {
		t.Fatalf("empty feed must not fail the resolve: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 42 {
		t.Fatalf("latest chapter = %v, want the record's 42", result.LatestChapter)
	}
	if result.LastUpdatedAt != nil {
		t.Fatalf("an empty feed has no date to offer, got %v", result.LastUpdatedAt)
	}
}

// feedEntry is one upload in a fake English feed. Entries are listed the way
// the API sorts them, highest chapter first.
type feedEntry struct {
	id       string
	chapter  string
	publish  string
	external bool
}

// serveFeed answers feed requests the way the live API does: includeExternalUrl
// unset keeps every chapter, =0 drops the external ones and =1 keeps only them;
// offset and limit page the list; and a window past offset+limit 10000 is
// refused. requests, when non-nil, counts the calls.
func serveFeed(entries []feedEntry, requests *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		query := r.URL.Query()
		offset, _ := strconv.Atoi(query.Get("offset"))
		limit, _ := strconv.Atoi(query.Get("limit"))
		if limit <= 0 || offset < 0 || offset+limit > 10000 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		kept := make([]feedEntry, 0, len(entries))
		for _, entry := range entries {
			switch query.Get("includeExternalUrl") {
			case "0":
				if entry.external {
					continue
				}
			case "1":
				if !entry.external {
					continue
				}
			}
			kept = append(kept, entry)
		}

		data := []map[string]any{}
		for i := offset; i < len(kept) && i < offset+limit; i++ {
			data = append(data, map[string]any{
				"id":         kept[i].id,
				"attributes": map[string]any{"chapter": kept[i].chapter, "publishAt": kept[i].publish},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
}

// lastChapter is the original run's final chapter and ignores translation: a
// series finished at 150 in its own language while the English scanlation is
// at 80 must report 80, dated with chapter 80's release. It used to report 150
// beside that date and then never move as the English chapters came out.
func TestMangaDexFeedChapterBeatsRecordLastChapter(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+id, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":         id,
				"attributes": map[string]any{"title": map[string]string{"en": "Test Title"}, "lastChapter": "150"},
			},
		})
	})
	mux.HandleFunc("/manga/"+id+"/feed", serveFeed([]feedEntry{
		{id: "c80", chapter: "80", publish: "2026-10-28T00:00:00+00:00"},
		{id: "c79", chapter: "79", publish: "2026-10-21T00:00:00+00:00"},
	}, nil))
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	result, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 80 {
		t.Fatalf("latest chapter = %v, want the English feed's 80", result.LatestChapter)
	}
	want := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	if result.LastUpdatedAt == nil || !result.LastUpdatedAt.Equal(want) {
		t.Fatalf("release date = %v, want chapter 80's %v", result.LastUpdatedAt, want)
	}
}

// Search results follow the resolve's precedence: the English feed's number
// first, the record's lastChapter only when the feed is empty, and no chapter
// at all when the feed could not be read (the form then asks the resolve).
func TestMangaDexSearchPrefersFeedChapterOverRecordLastChapter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/manga", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "aaa", "attributes": map[string]any{"title": map[string]string{"en": "Gamma One"}, "lastChapter": "150"}},
				{"id": "bbb", "attributes": map[string]any{"title": map[string]string{"en": "Gamma Two"}, "lastChapter": "42"}},
				{"id": "ccc", "attributes": map[string]any{"title": map[string]string{"en": "Gamma Three"}, "lastChapter": "99"}},
			},
		})
	})
	mux.HandleFunc("/manga/aaa/feed", serveFeed([]feedEntry{{id: "c80", chapter: "80", publish: "2026-10-28T00:00:00+00:00"}}, nil))
	mux.HandleFunc("/manga/bbb/feed", serveFeed(nil, nil))
	mux.HandleFunc("/manga/ccc/feed", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	results, err := connector.SearchByTitle(context.Background(), "gamma", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	want := map[string]float64{"aaa": 80, "bbb": 42}
	for _, result := range results {
		expected, hasChapter := want[result.SourceItemID]
		switch {
		case !hasChapter && result.LatestChapter != nil:
			t.Fatalf("%s: latest chapter = %v, want none", result.SourceItemID, *result.LatestChapter)
		case hasChapter && (result.LatestChapter == nil || *result.LatestChapter != expected):
			t.Fatalf("%s: latest chapter = %v, want %v", result.SourceItemID, result.LatestChapter, expected)
		}
	}
}

// An official external chapter (a MangaPlus release listed with a link out) is
// a released English chapter, so tracking counts it; leaving it out reported a
// simulcast series stalled at its last fan scan. The chapter-link lookup still
// skips it, since it has no MangaDex page to open.
func TestMangaDexTrackingCountsExternalChapters(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	externals := []feedEntry{
		{id: "ext-152", chapter: "152", publish: "2026-10-28T00:00:00+00:00", external: true},
		{id: "ext-151", chapter: "151", publish: "2026-10-21T00:00:00+00:00", external: true},
	}
	scans := []feedEntry{
		{id: "scan-100", chapter: "100", publish: "2024-01-01T00:00:00+00:00"},
	}

	cases := []struct {
		name        string
		entries     []feedEntry
		wantLatest  float64
		wantRelease time.Time
	}{
		{
			name:        "externals ahead of old fan scans",
			entries:     append(append([]feedEntry{}, externals...), scans...),
			wantLatest:  152,
			wantRelease: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			name:        "externals only",
			entries:     externals,
			wantLatest:  152,
			wantRelease: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			name:        "fan scans only",
			entries:     scans,
			wantLatest:  100,
			wantRelease: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/manga/"+id, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{"id": id, "attributes": map[string]any{"title": map[string]string{"en": "Test Title"}}},
				})
			})
			mux.HandleFunc("/manga/"+id+"/feed", serveFeed(tc.entries, nil))
			server := httptest.NewServer(mux)
			defer server.Close()

			connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
			result, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+id)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if result.LatestChapter == nil || *result.LatestChapter != tc.wantLatest {
				t.Fatalf("latest chapter = %v, want %v", result.LatestChapter, tc.wantLatest)
			}
			if result.LastUpdatedAt == nil || !result.LastUpdatedAt.Equal(tc.wantRelease) {
				t.Fatalf("release date = %v, want %v", result.LastUpdatedAt, tc.wantRelease)
			}

			for _, entry := range tc.entries {
				chapter, _ := strconv.ParseFloat(entry.chapter, 64)
				link, err := connector.ResolveChapterURL(context.Background(), "https://mangadex.org/title/"+id, chapter)
				if entry.external {
					if !errors.Is(err, connectors.ErrChapterNotFound) {
						t.Fatalf("external chapter %s: got (%q, %v), want ErrChapterNotFound", entry.chapter, link, err)
					}
					continue
				}
				if err != nil || link != "https://mangadex.org/chapter/"+entry.id {
					t.Fatalf("chapter %s: got (%q, %v), want its MangaDex page", entry.chapter, link, err)
				}
			}
		})
	}
}

// The feed has one entry per group upload, so a long series' older chapters sit
// past the first 500-entry page. The lookup pages on until it finds the
// chapter, and gives up as soon as a page ends below it or the feed runs out,
// rather than reading the whole feed for a number it does not carry.
func TestMangaDexChapterLookupPagesThroughTheFeed(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	// 600 chapters, two groups each: 1200 entries, three pages of 500.
	entries := make([]feedEntry, 0, 1200)
	for chapter := 600; chapter >= 1; chapter-- {
		for _, group := range []string{"a", "b"} {
			entries = append(entries, feedEntry{id: fmt.Sprintf("c%d-%s", chapter, group), chapter: strconv.Itoa(chapter)})
		}
	}

	cases := []struct {
		name         string
		chapter      float64
		wantID       string
		wantRequests int32
	}{
		{name: "recent chapter on the first page", chapter: 590, wantID: "c590-a", wantRequests: 1},
		{name: "older chapter on the second page", chapter: 120, wantID: "c120-a", wantRequests: 2},
		{name: "first chapter on the short last page", chapter: 1, wantID: "c1-a", wantRequests: 3},
		{name: "missing number stops once a page ends below it", chapter: 120.5, wantRequests: 2},
		{name: "missing number stops at the end of the feed", chapter: 0.5, wantRequests: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/manga/"+id+"/feed", serveFeed(entries, &requests))
			server := httptest.NewServer(mux)
			defer server.Close()

			connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
			link, err := connector.ResolveChapterURL(context.Background(), "https://mangadex.org/title/"+id, tc.chapter)
			if tc.wantID == "" {
				if !errors.Is(err, connectors.ErrChapterNotFound) {
					t.Fatalf("got (%q, %v), want ErrChapterNotFound", link, err)
				}
			} else if err != nil || !strings.HasSuffix(link, "/chapter/"+tc.wantID) {
				t.Fatalf("got (%q, %v), want the page of %s", link, err, tc.wantID)
			}
			if got := requests.Load(); got != tc.wantRequests {
				t.Fatalf("feed requests = %d, want %d", got, tc.wantRequests)
			}
		})
	}
}

// A feed that never drops below the target still ends at the API's
// offset+limit ceiling with a not-found verdict, rather than with the 400 the
// API answers for a window past it.
func TestMangaDexChapterLookupStopsAtTheFeedWindow(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	var requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+id+"/feed", func(w http.ResponseWriter, r *http.Request) {
		// A guard against a runaway loop: the ceiling allows 20 pages of 500.
		if requests.Add(1) > 25 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 || offset+limit > 10000 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, 0, limit)
		for i := 0; i < limit; i++ {
			data = append(data, map[string]any{
				"id":         fmt.Sprintf("c-%d", offset+i),
				"attributes": map[string]any{"chapter": "5000"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	link, err := connector.ResolveChapterURL(context.Background(), "https://mangadex.org/title/"+id, 10)
	if !errors.Is(err, connectors.ErrChapterNotFound) {
		t.Fatalf("got (%q, %v), want ErrChapterNotFound", link, err)
	}
	if got := requests.Load(); got != 20 {
		t.Fatalf("feed requests = %d, want 20 (offset+limit capped at 10000)", got)
	}
}
