package yamlconnector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestYAMLConnectorSearchResolveHealth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id":            "10",
				"title":         "Test Manga",
				"url":           "https://example.com/manga/10",
				"latestChapter": 11.5,
			}},
		})
	})
	mux.HandleFunc("/resolve", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"item": map[string]any{
				"id":            "10",
				"title":         "Test Manga",
				"url":           "https://example.com/manga/10",
				"latestChapter": 11.5,
			},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	enabled := true
	connector, err := NewConnector(Config{
		Key:          "example",
		Name:         "Example",
		Enabled:      &enabled,
		BaseURL:      server.URL,
		AllowedHosts: []string{"example.com"},
		HealthPath:   "/health",
		Search: struct {
			Path       string `yaml:"path"`
			QueryParam string `yaml:"query_param"`
			LimitParam string `yaml:"limit_param"`
		}{Path: "/search", QueryParam: "q", LimitParam: "limit"},
		Resolve: struct {
			Path     string `yaml:"path"`
			URLParam string `yaml:"url_param"`
		}{Path: "/resolve", URLParam: "url"},
		Response: struct {
			SearchItemsPath    string `yaml:"search_items_path"`
			ResolveItemPath    string `yaml:"resolve_item_path"`
			IDField            string `yaml:"id_field"`
			TitleField         string `yaml:"title_field"`
			URLField           string `yaml:"url_field"`
			LatestChapterField string `yaml:"latest_chapter_field"`
		}{
			SearchItemsPath:    "items",
			ResolveItemPath:    "item",
			IDField:            "id",
			TitleField:         "title",
			URLField:           "url",
			LatestChapterField: "latestChapter",
		},
	}, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new connector: %v", err)
	}

	if err := connector.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health failed: %v", err)
	}

	search, err := connector.SearchByTitle(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(search) != 1 {
		t.Fatalf("expected 1 search item, got %d", len(search))
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://example.com/manga/10")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "10" {
		t.Fatalf("expected source item id 10, got %s", resolved.SourceItemID)
	}
}
