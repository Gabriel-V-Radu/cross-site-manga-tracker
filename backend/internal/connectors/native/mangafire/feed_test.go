package mangafire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The feed is the home page's "Latest Updates" listing: the titles endpoint,
// signed like every other call, ordered by chapter_updated_at, keyed by hid.
func TestMangaFireRecentUpdatesReadsTheLatestUpdatesListing(t *testing.T) {
	var query map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/titles" {
			http.NotFound(w, r)
			return
		}
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"hid":"82wky","slug":"emperors-sword","title":"Emperor's Sword","latestChapter":132,"chapterUpdatedAt":"37m ago"},
			{"hid":"DKW","slug":"one-piece","title":"One Piece","latestChapter":1188.5,"chapterUpdatedAt":"2h ago"},
			{"hid":"nochap","slug":"announced","title":"Announced","chapterUpdatedAt":"3h ago"},
			{"hid":"","slug":"broken","latestChapter":3}
		]}`))
	}))
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})
	updates, err := connector.RecentUpdates(context.Background())
	if err != nil {
		t.Fatalf("recent updates: %v", err)
	}

	if got := query["order[chapter_updated_at]"]; len(got) != 1 || got[0] != "desc" {
		t.Fatalf("the listing must be asked newest first, got %v", query)
	}
	if got := query["limit"]; len(got) != 1 || got[0] != "100" {
		t.Fatalf("limit = %v, want 100", got)
	}
	if len(query["vrf"]) != 1 || query["vrf"][0] == "" {
		t.Fatalf("the request must be signed, got %v", query)
	}

	byKey := map[string]float64{}
	for _, update := range updates {
		byKey[update.Key] = update.Chapter
		if update.UpdatedAt != nil {
			t.Fatalf("the listing dates its rows only as relative text, got %v", update.UpdatedAt)
		}
	}
	if len(byKey) != 2 || byKey["82wky"] != 132 || byKey["dkw"] != 1188.5 {
		t.Fatalf("rows = %v, want 82wky:132 and dkw:1188.5 (a title without a chapter or a hid is no row)", byKey)
	}
}

// Every URL form a tracker may have stored names the same title.
func TestMangaFireFeedKeyIsTheTitleHid(t *testing.T) {
	connector := NewConnector()
	for _, rawURL := range []string{
		"https://mangafire.to/title/dkw-one-piece",
		"https://mangafire.to/title/DKW",
		"https://mangafire.to/manga/one-piecee.dkw",
		"https://mangafire.to/read/one-piecee.dkw/en/chapter-1",
	} {
		key, ok := connector.FeedKey(rawURL)
		if !ok || key != "dkw" {
			t.Fatalf("FeedKey(%q) = %q, %v; want dkw", rawURL, key, ok)
		}
	}
	if _, ok := connector.FeedKey("https://mangadex.org/title/dkw"); ok {
		t.Fatal("another site's URL has no key in this feed")
	}
}
