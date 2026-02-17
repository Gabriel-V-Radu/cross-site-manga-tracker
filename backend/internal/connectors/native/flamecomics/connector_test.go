package flamecomics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFlameComicsConnectorResolveAndSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/series/83">The Novel's Extra (Remake)</a>
  <a href="/series/83/cd9daeaf1eb9b6ca">Chapter 146 a day ago</a>
  <a href="/series/159">I Became the First Prince: Legend of Sword's Song</a>
  <a href="/series/159/e9b90701ae9541e0">Chapter 23 3 hours ago</a>
</body>
</html>`))
	})
	mux.HandleFunc("/series/83", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="The Novel's Extra (Remake) - Flame Comics">
  <meta property="og:image" content="https://flamecomics.xyz/_next/image?url=https%3A%2F%2Fcdn.flamecomics.xyz%2Fuploads%2Fimages%2Fseries%2F83%2Fthumbnail.png&w=1920&q=100">
</head>
<body>
  <a href="/series/83/cd9daeaf1eb9b6ca">Chapter 146</a>
  <span>February 16, 2026 3:49 PM</span>
  <a href="/series/83/4824f4f6a5dfb9ea">Chapter 145</a>
  <span>February 9, 2026 5:10 PM</span>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 5 * time.Second})

	if err := conn.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := conn.ResolveByURL(context.Background(), "https://flamecomics.xyz/series/83")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "83" {
		t.Fatalf("expected source id 83, got %s", resolved.SourceItemID)
	}
	if resolved.Title != "The Novel's Extra (Remake)" {
		t.Fatalf("expected title, got %s", resolved.Title)
	}
	if resolved.CoverImageURL != "https://cdn.flamecomics.xyz/uploads/images/series/83/thumbnail.png" {
		t.Fatalf("unexpected cover image %s", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 146 {
		t.Fatalf("expected latest chapter 146, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || resolved.LastUpdatedAt.Format("2006-01-02") != "2026-02-16" {
		t.Fatalf("expected release date 2026-02-16, got %v", resolved.LastUpdatedAt)
	}

	results, err := conn.SearchByTitle(context.Background(), "novel", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].SourceItemID != "83" {
		t.Fatalf("expected source id 83 in search, got %s", results[0].SourceItemID)
	}
}

func TestFlameComicsResolveChapterURLWithNestedAnchorMarkup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/series/22", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/series/22/2dd2ff4ef56a0e99"><span class="chapter-label">Chapter 13</span><span> November 21, 2024 8:49 AM</span></a>
  <a href="/series/22/75a51e16b796ed2d"><span>Chapter 12</span><span> November 21, 2024 8:49 AM</span></a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 5 * time.Second})

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://flamecomics.xyz/series/22", 13)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}

	if chapterURL != "https://flamecomics.xyz/series/22/2dd2ff4ef56a0e99" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}
}
