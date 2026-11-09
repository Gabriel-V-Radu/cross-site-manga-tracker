package asuracomic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// The home page lists the series updated last with links to their newest
// chapters. Rows are keyed by the slug without its hash suffix, because the
// suffix on the home page is not the one a stored link carries.
func TestAsuraRecentUpdatesReadsTheHomePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `<html><body>
			<a href="/comics/killer-pietro-f212c582">Killer Pietro</a>
			<a href="/comics/killer-pietro-f212c582/chapter/155">Chapter 155</a>
			<a href="/comics/killer-pietro-f212c582/chapter/156">Chapter 156</a>
			<a href="https://asurascans.com/comics/the-tutorial-is-too-hard-f212c582/chapter/296">Chapter 296</a>
			<a href="/comics/a-side-story-f212c582/chapter/12.5">Chapter 12.5</a>
			<a href="/comics/not-a-chapter-link-f212c582">Browse</a>
			<a href="/blog/chapter/7">Not a comic</a>
		</body></html>`)
	}))
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"asurascans.com"}, &http.Client{Timeout: 5 * time.Second})
	updates, err := connector.RecentUpdates(context.Background())
	if err != nil {
		t.Fatalf("recent updates: %v", err)
	}

	byKey := map[string]float64{}
	for _, update := range updates {
		byKey[update.Key] = update.Chapter
	}
	want := map[string]float64{"killer-pietro": 156, "the-tutorial-is-too-hard": 296, "a-side-story": 12.5}
	if len(byKey) != len(want) {
		t.Fatalf("rows = %v, want %v", byKey, want)
	}
	for key, chapter := range want {
		if byKey[key] != chapter {
			t.Fatalf("rows = %v, want %s at %v", byKey, key, chapter)
		}
	}
}

// A home page that loads and lists no chapter link is a redesign, and must
// not read as "nothing was updated".
func TestAsuraRecentUpdatesFailsOnAHomePageWithoutChapterLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><body><a href="/comics/killer-pietro-f212c582">Killer Pietro</a></body></html>`)
	}))
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"asurascans.com"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := connector.RecentUpdates(context.Background()); !errors.Is(err, connectors.ErrNoChaptersListed) {
		t.Fatalf("err = %v, want ErrNoChaptersListed", err)
	}
}

// A stored link and the home page name the same series under different hash
// suffixes; the key is what they share.
func TestAsuraFeedKeyDropsTheHashSuffix(t *testing.T) {
	connector := NewConnector()
	for _, rawURL := range []string{
		"https://asurascans.com/comics/killer-pietro-19cdf401",
		"https://asuracomic.net/series/killer-pietro-7f873ca6",
		"https://asurascans.com/comics/killer-pietro",
	} {
		key, ok := connector.FeedKey(rawURL)
		if !ok || key != "killer-pietro" {
			t.Fatalf("FeedKey(%q) = %q, %v; want killer-pietro", rawURL, key, ok)
		}
	}
	if _, ok := connector.FeedKey("https://asurascans.com/browse?q=x"); ok {
		t.Fatal("a URL that is not a series has no key")
	}
}
