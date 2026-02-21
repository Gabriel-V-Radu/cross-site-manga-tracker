package mangadex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
