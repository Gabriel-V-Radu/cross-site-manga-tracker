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
					"id":         "abc",
					"attributes": map[string]any{"title": map[string]string{"en": "Alpha"}},
				},
				{
					"id": "def",
					"attributes": map[string]any{
						"title":       map[string]string{"en": "Beta"},
						"lastChapter": "13.5",
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

	results, err := connector.SearchByTitle(context.Background(), "alpha", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].SourceItemID == "abc" {
		if results[0].LatestChapter == nil || *results[0].LatestChapter != 7.5 {
			t.Fatalf("expected fallback latest chapter 7.5 for abc, got %v", results[0].LatestChapter)
		}
	}
	if results[1].SourceItemID == "abc" {
		if results[1].LatestChapter == nil || *results[1].LatestChapter != 7.5 {
			t.Fatalf("expected fallback latest chapter 7.5 for abc, got %v", results[1].LatestChapter)
		}
	}

	if results[0].SourceItemID == "def" {
		if results[0].LatestChapter == nil || *results[0].LatestChapter != 13.5 {
			t.Fatalf("expected latest chapter 13.5 for def, got %v", results[0].LatestChapter)
		}
	}
	if results[1].SourceItemID == "def" {
		if results[1].LatestChapter == nil || *results[1].LatestChapter != 13.5 {
			t.Fatalf("expected latest chapter 13.5 for def, got %v", results[1].LatestChapter)
		}
	}
}
