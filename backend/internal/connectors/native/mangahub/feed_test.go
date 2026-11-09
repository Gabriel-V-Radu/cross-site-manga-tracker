package mangahub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMangaHubRecentUpdatesReadsTheLatestListing(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &payload)
		asked = payload.Query
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"search":{"rows":[
			{"slug":"Dandadan","latestChapter":252,"updatedDate":"2026-11-09T15:11:19.000Z"},
			{"slug":"no-chapter-yet","latestChapter":null,"updatedDate":"2026-11-09T15:00:00.000Z"},
			{"slug":"global-martial-arts","latestChapter":379,"updatedDate":""}
		]}}}`))
	}))
	defer server.Close()

	connector := NewConnectorWithOptions("https://mangahub.io", server.URL, []string{"mangahub.io"}, &http.Client{Timeout: 5 * time.Second})
	updates, err := connector.RecentUpdates(context.Background())
	if err != nil {
		t.Fatalf("recent updates: %v", err)
	}

	if !strings.Contains(asked, "mod:LATEST") || !strings.Contains(asked, "x:m01") || !strings.Contains(asked, "limit:50") {
		t.Fatalf("the query must be the latest-updates listing of the mangahub.io catalog, got %q", asked)
	}
	if len(updates) != 2 {
		t.Fatalf("expected a row per series with a chapter, got %+v", updates)
	}
	if updates[0].Key != "dandadan" || updates[0].Chapter != 252 {
		t.Fatalf("first row = %+v", updates[0])
	}
	wantAt := time.Date(2026, 11, 9, 15, 11, 19, 0, time.UTC)
	if updates[0].UpdatedAt == nil || !updates[0].UpdatedAt.Equal(wantAt) {
		t.Fatalf("first row dated %v, want %v", updates[0].UpdatedAt, wantAt)
	}
	if updates[1].Key != "global-martial-arts" || updates[1].Chapter != 379 || updates[1].UpdatedAt != nil {
		t.Fatalf("second row = %+v, want an undated 379", updates[1])
	}
}

// A rate limit comes back as HTTP 200 with a GraphQL errors array; the feed
// must report it as a failure, not as an empty listing.
func TestMangaHubRecentUpdatesReportsAGraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field \"mod\""}]}`))
	}))
	defer server.Close()

	connector := NewConnectorWithOptions("https://mangahub.io", server.URL, []string{"mangahub.io"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := connector.RecentUpdates(context.Background()); err == nil {
		t.Fatal("a refused query must be an error")
	}
}

func TestMangaHubFeedKeyIsTheSeriesSlug(t *testing.T) {
	connector := NewConnector()
	for _, rawURL := range []string{
		"https://mangahub.io/manga/Dandadan",
		"https://mangahub.io/chapter/dandadan/chapter-252",
	} {
		key, ok := connector.FeedKey(rawURL)
		if !ok || key != "dandadan" {
			t.Fatalf("FeedKey(%q) = %q, %v; want dandadan", rawURL, key, ok)
		}
	}
	if _, ok := connector.FeedKey("https://api.mghcdn.com/manga/dandadan"); ok {
		t.Fatal("the former API host serves no series pages")
	}
}
