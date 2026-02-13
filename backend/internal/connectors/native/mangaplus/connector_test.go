package mangaplus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMangaPlusConnector(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/title_list/allV2", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": map[string]any{
				"allTitlesView": map[string]any{
					"titles": []map[string]any{
						{"titleId": 100, "name": "One Piece"},
						{"titleId": 200, "name": "Blue Box"},
					},
				},
			},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangaplus.shueisha.co.jp"}, &http.Client{Timeout: 5 * time.Second})

	if err := connector.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangaplus.shueisha.co.jp/titles/100")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Title != "One Piece" {
		t.Fatalf("expected One Piece, got %s", resolved.Title)
	}

	results, err := connector.SearchByTitle(context.Background(), "blue", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].SourceItemID != "200" {
		t.Fatalf("expected source item id 200, got %s", results[0].SourceItemID)
	}
}
