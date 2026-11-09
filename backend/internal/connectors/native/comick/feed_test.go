package comick

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComicKRecentUpdatesReadsTheNewestChaptersListing(t *testing.T) {
	var query map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chapter" {
			http.NotFound(w, r)
			return
		}
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"chap":"267","created_at":"2026-11-09T15:02:11.000Z","md_comics":{"hid":"h1","slug":"Manager-Kim"}},
			{"chap":"266","created_at":"2026-11-09T15:01:00.000Z","md_comics":{"hid":"h1","slug":"manager-kim"}},
			{"chap":null,"created_at":"2026-11-09T15:00:00.000Z","md_comics":{"hid":"h2","slug":"a-oneshot"}},
			{"chap":"143","publish_at":"2026-11-08T15:02:00.000Z","created_at":"2026-11-08T15:05:00.000Z","md_comics":{"hid":"h3","slug":"spy-x-family"}}
		]`))
	}))
	defer server.Close()

	connector := NewConnectorWithOptions("", server.URL, []string{"comick.dev"}, &http.Client{Timeout: 5 * time.Second})
	updates, err := connector.RecentUpdates(context.Background())
	if err != nil {
		t.Fatalf("recent updates: %v", err)
	}

	if got := query["lang"]; len(got) != 1 || got[0] != "en" {
		t.Fatalf("the listing must be English only, got %v", query)
	}
	if got := query["order"]; len(got) != 1 || got[0] != "new" {
		t.Fatalf("the listing must be newest first, got %v", query)
	}

	if len(updates) != 2 {
		t.Fatalf("expected one row per comic with a numbered chapter, got %+v", updates)
	}
	if updates[0].Key != "manager-kim" || updates[0].Chapter != 267 {
		t.Fatalf("first row = %+v, want manager-kim at its highest listed chapter, 267", updates[0])
	}
	if updates[1].Key != "spy-x-family" || updates[1].Chapter != 143 {
		t.Fatalf("second row = %+v", updates[1])
	}
	wantAt := time.Date(2026, 11, 8, 15, 2, 0, 0, time.UTC)
	if updates[1].UpdatedAt == nil || !updates[1].UpdatedAt.Equal(wantAt) {
		t.Fatalf("a row is dated from publish_at when the entry has one, got %v", updates[1].UpdatedAt)
	}
}

func TestComicKFeedKeyIsTheComicSlug(t *testing.T) {
	connector := NewConnector()
	for _, rawURL := range []string{
		"https://comick.dev/comic/Manager-Kim",
		"https://comick.io/comic/manager-kim/abc12-chapter-267-en",
	} {
		key, ok := connector.FeedKey(rawURL)
		if !ok || key != "manager-kim" {
			t.Fatalf("FeedKey(%q) = %q, %v; want manager-kim", rawURL, key, ok)
		}
	}
	if _, ok := connector.FeedKey("https://comick.dev/user/someone"); ok {
		t.Fatal("a URL that is not a comic has no key")
	}
}
