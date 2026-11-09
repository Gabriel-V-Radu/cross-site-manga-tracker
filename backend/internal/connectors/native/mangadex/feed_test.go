package mangadex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// One request to the site-wide chapter listing yields a row per title: its
// highest chapter in the page, dated from that chapter's entry.
func TestMangaDexRecentUpdatesGroupsTheListingByTitle(t *testing.T) {
	var query map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chapter" {
			http.NotFound(w, r)
			return
		}
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"c1","attributes":{"chapter":"81","publishAt":"2026-11-09T17:31:26+00:00"},
			 "relationships":[{"id":"g1","type":"scanlation_group"},{"id":"D8F1D7DA-8bb1-407b-8be3-10ac2894d3c6","type":"manga"}]},
			{"id":"c2","attributes":{"chapter":"80.5","publishAt":"2026-11-09T12:00:00+00:00"},
			 "relationships":[{"id":"d8f1d7da-8bb1-407b-8be3-10ac2894d3c6","type":"manga"}]},
			{"id":"c3","attributes":{"chapter":null,"publishAt":"2026-11-09T11:00:00+00:00"},
			 "relationships":[{"id":"3e11f43e-3d91-47c8-a2ad-e2f23d36ad4c","type":"manga"}]},
			{"id":"c4","attributes":{"chapter":"260","readableAt":"2026-11-09T16:00:15+00:00"},
			 "relationships":[{"id":"3e11f43e-3d91-47c8-a2ad-e2f23d36ad4c","type":"manga"}]}
		]}`))
	}))
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	updates, err := connector.RecentUpdates(context.Background())
	if err != nil {
		t.Fatalf("recent updates: %v", err)
	}

	if got := query["translatedLanguage[]"]; len(got) != 1 || got[0] != "en" {
		t.Fatalf("the listing must be English only, got %v", got)
	}
	if got := query["order[readableAt]"]; len(got) != 1 || got[0] != "desc" {
		t.Fatalf("the listing must be newest first, got %v", query)
	}
	if got := query["contentRating[]"]; len(got) != 4 {
		t.Fatalf("every content rating must be listed, got %v", got)
	}
	if got := query["includeFutureUpdates"]; len(got) != 1 || got[0] != "0" {
		t.Fatalf("chapters scheduled for later must be left out, got %v", got)
	}

	if len(updates) != 2 {
		t.Fatalf("expected one row per title, got %+v", updates)
	}
	first, second := updates[0], updates[1]
	if first.Key != "3e11f43e-3d91-47c8-a2ad-e2f23d36ad4c" || first.Chapter != 260 {
		t.Fatalf("first row = %+v, want the numbered chapter 260 of 3e11f43e", first)
	}
	wantAt := time.Date(2026, 11, 9, 16, 0, 15, 0, time.UTC)
	if first.UpdatedAt == nil || !first.UpdatedAt.Equal(wantAt) {
		t.Fatalf("first row dated %v, want %v", first.UpdatedAt, wantAt)
	}
	if second.Key != "d8f1d7da-8bb1-407b-8be3-10ac2894d3c6" || second.Chapter != 81 {
		t.Fatalf("second row = %+v, want chapter 81, the title's highest in the page, under its lower-cased id", second)
	}
}

func TestMangaDexFeedKeyIsTheTitleID(t *testing.T) {
	connector := NewConnector()
	key, ok := connector.FeedKey(" https://mangadex.org/title/D8F1D7DA-8bb1-407b-8be3-10ac2894d3c6/isekai-ojisan ")
	if !ok || key != "d8f1d7da-8bb1-407b-8be3-10ac2894d3c6" {
		t.Fatalf("FeedKey = %q, %v", key, ok)
	}
	if _, ok := connector.FeedKey("https://mangadex.org/chapter/c831a88c-66d8-474f-a252-99ae88739788"); ok {
		t.Fatal("a chapter URL names no title")
	}
}
