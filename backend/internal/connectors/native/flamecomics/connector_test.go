package flamecomics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
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
  <div>Alternative Names: The Novel's Additional Story | TNER</div>
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
	if len(resolved.RelatedTitles) == 0 {
		t.Fatalf("expected related titles from alternative names")
	}
	for _, related := range resolved.RelatedTitles {
		if related == resolved.Title {
			t.Fatalf("did not expect primary title in related titles: %q", related)
		}
	}

	results, err := conn.SearchByTitle(context.Background(), "additional story", 8)
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

// TestFlameComicsSearchReadsListingMatchesFirst pins the search order: every
// series page costs a turn in the per-host pacing, so the entries whose listed
// title already answers the query are read before the rest of /latest, and a
// series anchored twice is read once. Walking the listing in page order spent
// a dashboard search's whole budget on the first few entries and answered "no
// matches" for a title ninth on the page.
func TestFlameComicsSearchReadsListingMatchesFirst(t *testing.T) {
	var listing strings.Builder
	listing.WriteString(`<!DOCTYPE html><html><body>`)
	for id := 1; id <= 30; id++ {
		title := fmt.Sprintf("Series Number %d", id)
		if id == 9 {
			title = "Omniscient Reader"
		}
		fmt.Fprintf(&listing, `<a href="/series/%d">%s</a>`, id, title)
	}
	// The same series anchored a second time, under its alternate name.
	listing.WriteString(`<a href="/series/9">Omniscient Reader's Viewpoint</a>`)
	listing.WriteString(`</body></html>`)

	var mu sync.Mutex
	var fetched []string
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listing.String()))
	})
	mux.HandleFunc("/series/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/series/")
		mu.Lock()
		fetched = append(fetched, id)
		mu.Unlock()
		title := "Series Number " + id
		if id == "9" {
			title = "Omniscient Reader"
		}
		fmt.Fprintf(w, `<html><head><meta property="og:title" content="%s - Flame Comics"></head><body><a href="/series/%s/abcdef12">Chapter 10</a></body></html>`, title, id)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 5 * time.Second})
	results, err := conn.SearchByTitle(context.Background(), "omniscient reader", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceItemID != "9" {
		t.Fatalf("expected series 9 as the only result, got %+v", results)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fetched) == 0 || fetched[0] != "9" {
		t.Fatalf("expected the listing match to be read first, got fetch order %v", fetched)
	}
	reads := 0
	for _, id := range fetched {
		if id == "9" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("expected series 9 to be read once, read %d times", reads)
	}
	if len(fetched) != 30 {
		t.Fatalf("expected the rest of the listing to be read for alternate titles while budget remained, got %d reads", len(fetched))
	}
}

// TestFlameComicsSearchReportsATimeoutInsteadOfNoMatches pins that a search
// whose budget ran out before anything matched returns the deadline as an
// error, so the form says the search failed instead of "No matches found".
func TestFlameComicsSearchReportsATimeoutInsteadOfNoMatches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/series/1">Slow Series</a><a href="/series/2">Another Slow Series</a></body></html>`))
	})
	mux.HandleFunc("/series/", func(_ http.ResponseWriter, r *http.Request) {
		// Held until the client gives up, with a cap so a missed cancellation
		// cannot hang the test.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	results, err := conn.SearchByTitle(ctx, "slow series", 8)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the deadline as the error, got results %+v and error %v", results, err)
	}
}

// TestFlameComicsHealthCheckRequiresAParsedSeries pins that a /latest page
// which loads but carries no series link (a markup change) fails the health
// check instead of reporting the connector healthy.
func TestFlameComicsHealthCheckRequiresAParsedSeries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><div id="__next"></div></body></html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 5 * time.Second})
	if err := conn.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected a /latest page with no series link to fail the health check")
	}
}

// TestFlameComicsChapterlessSeriesPageFailsResolveButNotSearch pins both sides
// of the no-chapters verdict: the poll path reports a failed resolve, while
// search still lists a series that has no chapter out yet.
func TestFlameComicsChapterlessSeriesPageFailsResolveButNotSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><a href="/series/200">Brand New Series</a></body></html>`))
	})
	mux.HandleFunc("/series/200", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Brand New Series - Flame Comics">
</head>
<body><div>No chapters yet</div></body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := conn.ResolveByURL(context.Background(), "https://flamecomics.xyz/series/200")
	if !errors.Is(err, connectors.ErrNoChaptersListed) {
		t.Fatalf("expected the no-chapters verdict, got result %v and error %v", resolved, err)
	}
	if resolved != nil {
		t.Fatalf("a failed resolve must not hand back a result, got %+v", resolved)
	}

	results, err := conn.SearchByTitle(context.Background(), "brand new series", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceItemID != "200" || results[0].LatestChapter != nil {
		t.Fatalf("expected the chapterless series in the results with no chapter, got %+v", results)
	}
}
