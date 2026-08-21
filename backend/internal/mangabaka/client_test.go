package mangabaka

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const searchFixture = `{"status":200,"data":[
	{"id":145,"state":"active","title":"Nano Machine","native_title":"나노마신","romanized_title":"Nano Machine",
	 "secondary_titles":{"unknown":[{"title":"Nano Mashin"},{"title":"나노마신"},{"title":"Nano Machine"}]},
	 "source":{"manga_updates":{"id":"01w7hvo"},"my_anime_list":{"id":147863}}},
	{"id":999,"state":"merged","merged_with":145,"title":"Nano Machine (duplicate)",
	 "secondary_titles":{},"source":{"manga_updates":{"id":"zzzzzz"}}},
	{"id":500,"state":"active","title":"Machine Uprising","secondary_titles":{},
	 "source":{"manga_updates":{"id":null}}}
]}`

func newTestClient(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/series/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(searchFixture))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewClientWithOptions(server.URL, &http.Client{Timeout: 5 * time.Second})
}

func TestSearchParsesTitlesAndMangaUpdatesID(t *testing.T) {
	client := newTestClient(t)

	results, err := client.Search(context.Background(), "nano machine", 8)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// The merged record is dropped; the active ones survive.
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (merged record dropped)", len(results))
	}

	nano := results[0]
	if nano.Title != "Nano Machine" || nano.ID != 145 {
		t.Fatalf("unexpected first record: %+v", nano)
	}
	if nano.MangaUpdatesID != "01w7hvo" {
		t.Fatalf("mangaupdates id = %q", nano.MangaUpdatesID)
	}
	if nano.MangaUpdatesURL() != "https://www.mangaupdates.com/series/01w7hvo" {
		t.Fatalf("mangaupdates url = %q", nano.MangaUpdatesURL())
	}

	// Titles: main + secondary, deduplicated ("Nano Machine" appears twice in
	// the payload), native/non-Latin kept — filtering is the caller's choice.
	found := map[string]bool{}
	for _, title := range nano.Titles {
		found[title] = true
	}
	if !found["Nano Machine"] || !found["Nano Mashin"] || !found["나노마신"] {
		t.Fatalf("titles = %v", nano.Titles)
	}
	if len(nano.Titles) != 3 {
		t.Fatalf("titles not deduplicated: %v", nano.Titles)
	}

	// A null manga_updates id decodes to the empty string.
	if results[1].MangaUpdatesID != "" || results[1].MangaUpdatesURL() != "" {
		t.Fatalf("null id should map to empty: %+v", results[1])
	}
}
