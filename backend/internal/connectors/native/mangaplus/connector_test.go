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
				"allTitlesViewV2": map[string]any{
					"AllTitlesGroup": []map[string]any{
						{
							"theTitle": "A",
							"titles": []map[string]any{
								{"titleId": 100, "name": "One Piece"},
								{"titleId": 200, "name": "Blue Box"},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/title_detailV3", func(w http.ResponseWriter, r *http.Request) {
		titleID := r.URL.Query().Get("title_id")
		if titleID == "100" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": map[string]any{
					"titleDetailView": map[string]any{
						"chapterListGroup": []map[string]any{
							{
								"chapterNumbers":   "111",
								"firstChapterList": []map[string]any{{"name": "#001"}},
							},
						},
					},
				},
			})
			return
		}
		if titleID == "200" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": map[string]any{
					"titleDetailView": map[string]any{
						"chapterListGroup": []map[string]any{
							{
								"chapterNumbers": "10",
								"midChapterList": []map[string]any{{"name": "#012.5"}},
							},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
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
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 111 {
		t.Fatalf("expected latest chapter 111, got %v", resolved.LatestChapter)
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
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 12.5 {
		t.Fatalf("expected latest chapter 12.5, got %v", results[0].LatestChapter)
	}
}
