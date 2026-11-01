package asuracomic

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

func TestAsuraComicConnectorResolveAndSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("q")
		if name == "nano" {
			_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/comics/nano-machine-7f873ca6">ONGOING MANHWA Nano Machine Chapter 304 9.5</a>
  <a href="/comics/another-title-12345678">Another Title</a>
</body>
</html>`))
			return
		}

		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})

	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
	<meta property="og:image" content="https://cdn.asurascans.com/asura-images/covers/nano-social.webp">
</head>
<body>
	<img src="https://cdn.asurascans.com/asura-images/covers/nano-rendered-400.webp" alt="Nano Machine">
  <h1>Nano Machine</h1>
  <div>Alternative Names: Mechanical Cultivator | Nano Machine Reloaded</div>
  <div>Updated On</div><div>February 17th 2026</div>
  <a href="/comics/nano-machine-7f873ca6/chapter/303">Chapter 303</a>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	mux.HandleFunc("/comics/another-title-12345678", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <title>Another Title - Asura Scans</title>
</head>
<body>
  <a href="/comics/another-title-12345678/chapter/10">Chapter 10</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})

	if err := conn.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "nano-machine-7f873ca6" {
		t.Fatalf("expected source id nano-machine-7f873ca6, got %s", resolved.SourceItemID)
	}
	if resolved.Title != "Nano Machine" {
		t.Fatalf("expected title Nano Machine, got %s", resolved.Title)
	}
	if resolved.CoverImageURL != "https://cdn.asurascans.com/asura-images/covers/nano-rendered-400.webp" {
		t.Fatalf("unexpected cover image: %s", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 304 {
		t.Fatalf("expected latest chapter 304, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || resolved.LastUpdatedAt.Format("2006-01-02") != "2026-02-17" {
		t.Fatalf("expected updated date 2026-02-17, got %v", resolved.LastUpdatedAt)
	}

	results, err := conn.SearchByTitle(context.Background(), "nano", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].SourceItemID != "nano-machine-7f873ca6" {
		t.Fatalf("unexpected source id %s", results[0].SourceItemID)
	}
	if results[0].CoverImageURL != "https://cdn.asurascans.com/asura-images/covers/nano-rendered-400.webp" {
		t.Fatalf("unexpected search cover image: %s", results[0].CoverImageURL)
	}
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 304 {
		t.Fatalf("expected latest chapter 304 from search, got %v", results[0].LatestChapter)
	}
}

func TestAsuraComicConnectorFallsBackToMetaCoverImage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
  <meta property="og:image" content="https://cdn.asurascans.com/asura-images/covers/nano-social.webp">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.CoverImageURL != "https://cdn.asurascans.com/asura-images/covers/nano-social.webp" {
		t.Fatalf("unexpected fallback cover image: %s", resolved.CoverImageURL)
	}
}

func TestAsuraComicConnectorRejectsNonAsuraURLs(t *testing.T) {
	conn := NewConnectorWithOptions("https://asurascans.com", []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	_, err := conn.ResolveByURL(context.Background(), "https://example.com/series/nano-machine-11b89554")
	if err == nil {
		t.Fatalf("expected error for non-asuracomic url")
	}
}

func TestAsuraComicConnectorSupportsBareChapterPathPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/chapter/298">Chapter 298 February 4th 2026</a>
  <a href="/chapter/299">Chapter 299 February 11th 2026</a>
  <div>Updated On</div><div>February 17th 2026</div>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 299 {
		t.Fatalf("expected latest chapter 299, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || resolved.LastUpdatedAt.Format("2006-01-02") != "2026-02-11" {
		t.Fatalf("expected latest chapter release date 2026-02-11, got %v", resolved.LastUpdatedAt)
	}
}

func TestAsuraComicConnectorUsesPublishedAtForLatestChapter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/298">Chapter 298 February 4th 2026</a>
  <a href="/comics/nano-machine-7f873ca6/chapter/299">Chapter 299 February 11th 2026</a>
  <script>self.__next_f.push([1,"{\"chapters\":[{\"name\":298,\"title\":\"A\",\"id\":1001,\"published_at\":\"2026-02-04T15:10:00.000000Z\"},{\"name\":299,\"title\":\"B\",\"id\":1002,\"published_at\":\"2026-02-11T16:44:55.000000Z\"}]}"])</script>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected published_at timestamp for latest chapter")
	}

	expected := time.Date(2026, time.February, 11, 16, 44, 55, 0, time.UTC)
	if !resolved.LastUpdatedAt.Equal(expected) {
		t.Fatalf("expected latest chapter release time %s, got %s", expected.Format(time.RFC3339), resolved.LastUpdatedAt.UTC().Format(time.RFC3339))
	}
}

func TestAsuraComicConnectorUsesPublishedAtFromHTMLSerializedChapters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/a-villains-will-to-survive-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="A Villain's Will to Survive - Asura Scans">
</head>
<body>
  <astro-island props="{&quot;chapters&quot;:[3,[[0,{&quot;number&quot;:[0,49],&quot;published_at&quot;:[0,&quot;2026-03-20T14:38:02Z&quot;]}],[0,{&quot;number&quot;:[0,48],&quot;published_at&quot;:[0,&quot;2026-03-14T14:06:02Z&quot;]}]]}"></astro-island>
  <a href="/comics/a-villains-will-to-survive-7f873ca6/chapter/49">Chapter 49</a>
  <div class="text-sm text-white/40">1 hour ago</div>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/a-villains-will-to-survive-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	// The site serves the raw apostrophe inside a double-quoted og:title; the
	// title used to be cut off there ("A Villain").
	if resolved.Title != "A Villain's Will to Survive" {
		t.Fatalf("expected the full apostrophe title, got %q", resolved.Title)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 49 {
		t.Fatalf("expected latest chapter 49, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected published_at timestamp for latest chapter")
	}

	expected := time.Date(2026, time.March, 20, 14, 38, 2, 0, time.UTC)
	if !resolved.LastUpdatedAt.Equal(expected) {
		t.Fatalf("expected latest chapter release time %s, got %s", expected.Format(time.RFC3339), resolved.LastUpdatedAt.UTC().Format(time.RFC3339))
	}
}

func TestAsuraComicConnectorSearchSupportsSeriesHrefWithoutLeadingSlash(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "leveling solo" {
			if r.URL.Query().Get("q") != "leveling solo" {
				_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
				return
			}
		}

		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="comics/solo-leveling-ragnarok-c739e802">Solo Leveling: Ragnarok</a>
  <a href="comics/solo-leveling-26b0cf1b">Solo Leveling</a>
</body>
</html>`))
	})

	mux.HandleFunc("/comics/solo-leveling-ragnarok-c739e802", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Solo Leveling: Ragnarok - Asura Scans">
</head>
<body>
  <a href="/comics/solo-leveling-ragnarok-c739e802/chapter/68">Chapter 68</a>
</body>
</html>`))
	})

	mux.HandleFunc("/comics/solo-leveling-26b0cf1b", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Solo Leveling - Asura Scans">
</head>
<body>
  <a href="/comics/solo-leveling-26b0cf1b/chapter/200">Chapter 200</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	results, err := conn.SearchByTitle(context.Background(), "leveling solo", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestAsuraComicConnectorFallsBackFromLegacySeriesURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "nano machine" {
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
			return
		}

		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/comics/nano-machine-7f873ca6">Nano Machine</a>
</body>
</html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/comics/nano-machine-11b89554" {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asuracomic.net/series/nano-machine-11b89554")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "nano-machine-7f873ca6" {
		t.Fatalf("expected canonical source id, got %s", resolved.SourceItemID)
	}

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://asuracomic.net/series/nano-machine-11b89554", 304)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if chapterURL != server.URL+"/comics/nano-machine-7f873ca6/chapter/304" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}
}

// TestAsuraComicResolveChapterURLVerifiesAgainstTheSeriesPage pins that a
// chapter link is a verified claim, not a constructed guess: a chapter the
// series page lists resolves, one beyond its latest answers the typed
// not-found verdict so the reader chain can hand the turn to the next site.
func TestAsuraComicResolveChapterURLVerifiesAgainstTheSeriesPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/303">Chapter 303</a>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://asuracomic.net/comics/nano-machine-7f873ca6", 303)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if chapterURL != server.URL+"/comics/nano-machine-7f873ca6/chapter/303" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}

	_, err = conn.ResolveChapterURL(context.Background(), "https://asuracomic.net/comics/nano-machine-7f873ca6", 305)
	if err == nil {
		t.Fatalf("expected a chapter beyond the listing to be refused")
	}
	if !errors.Is(err, connectors.ErrChapterNotFound) {
		t.Fatalf("expected the typed not-found verdict, got %v", err)
	}
}

// TestAsuraComicResolveChapterURLRefusesChaptersThePageDoesNotList pins that a
// number at or below the latest is linked only when the page carries it. The
// upper bound alone built links for chapters Asura never ran (a .5 extra from
// another site), which 404 and were cached as verified hits. Below the lowest
// number on the page the link is still built, since the page may carry only
// the newest part of the run, and chapters known only from the serialized
// island props count as carried.
func TestAsuraComicResolveChapterURLRefusesChaptersThePageDoesNotList(t *testing.T) {
	var links strings.Builder
	for chapter := 50; chapter >= 40; chapter-- {
		if chapter == 47 {
			continue
		}
		fmt.Fprintf(&links, `<a href="/comics/nano-machine-7f873ca6/chapter/%d">Chapter %d</a>`, chapter, chapter)
	}
	// A "first chapter" button plus the newest rows, with the full run only in
	// the island's props.
	var props strings.Builder
	for chapter := 30; chapter >= 1; chapter-- {
		if props.Len() > 0 {
			props.WriteString(",")
		}
		fmt.Fprintf(&props, `[0,{&quot;number&quot;:[0,%d],&quot;published_at&quot;:[0,&quot;2026-01-01T00:00:00Z&quot;]}]`, chapter)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Nano Machine - Asura Scans"></head><body>` + links.String() + `</body></html>`))
	})
	mux.HandleFunc("/comics/island-series-12345678", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Island Series - Asura Scans"></head><body>` +
			`<astro-island props="{&quot;chapters&quot;:[1,[` + props.String() + `]]}"></astro-island>` +
			`<a href="/comics/island-series-12345678/chapter/1">First Chapter</a>` +
			`<a href="/comics/island-series-12345678/chapter/30">Chapter 30</a>` +
			`<a href="/comics/island-series-12345678/chapter/29">Chapter 29</a>` +
			`</body></html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})

	cases := []struct {
		name    string
		series  string
		chapter float64
		carried bool
	}{
		{name: "listed chapter", series: "nano-machine-7f873ca6", chapter: 46, carried: true},
		{name: "lowest listed chapter", series: "nano-machine-7f873ca6", chapter: 40, carried: true},
		{name: "decimal extra from another site", series: "nano-machine-7f873ca6", chapter: 45.5, carried: false},
		{name: "gap inside the listed range", series: "nano-machine-7f873ca6", chapter: 47, carried: false},
		{name: "below the lowest listed chapter", series: "nano-machine-7f873ca6", chapter: 12, carried: true},
		{name: "chapter only in the island props", series: "island-series-12345678", chapter: 15, carried: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://asurascans.com/comics/"+tc.series, tc.chapter)
			if !tc.carried {
				if !errors.Is(err, connectors.ErrChapterNotFound) {
					t.Fatalf("expected the typed not-found verdict, got url %q and error %v", chapterURL, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve chapter url failed: %v", err)
			}
			want := server.URL + "/comics/" + tc.series + "/chapter/" + connectors.FormatChapter(tc.chapter)
			if chapterURL != want {
				t.Fatalf("expected %s, got %s", want, chapterURL)
			}
		})
	}
}

// TestAsuraComicSearchFetchesOnlyMatchingSeries pins that the match is settled
// from the browse page before any series page is fetched. Every fetch costs a
// turn in the per-host pacing, and reading the nav and sidebar /comics/ links
// first used up a dashboard search's budget before the real hit got its turn.
func TestAsuraComicSearchFetchesOnlyMatchingSeries(t *testing.T) {
	var mu sync.Mutex
	var fetched []string
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <nav>
    <a href="/comics/popular-one-11111111">Popular One</a>
    <a href="/comics/popular-two-22222222">Popular Two</a>
    <a href="/comics/popular-three-33333333">Popular Three</a>
    <a href="/comics/popular-four-44444444">Popular Four</a>
  </nav>
  <a href="/comics/omniscient-reader-7f873ca6">Omniscient Reader</a>
</body>
</html>`))
	})
	mux.HandleFunc("/comics/", func(w http.ResponseWriter, r *http.Request) {
		seriesID := strings.TrimPrefix(r.URL.Path, "/comics/")
		mu.Lock()
		fetched = append(fetched, seriesID)
		mu.Unlock()
		fmt.Fprintf(w, `<html><head><meta property="og:title" content="Omniscient Reader - Asura Scans"></head><body><a href="/comics/%s/chapter/250">Chapter 250</a></body></html>`, seriesID)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	results, err := conn.SearchByTitle(context.Background(), "omniscient reader", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceItemID != "omniscient-reader-7f873ca6" {
		t.Fatalf("expected the one matching series, got %+v", results)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fetched) != 1 || fetched[0] != "omniscient-reader-7f873ca6" {
		t.Fatalf("expected only the matching series page to be fetched, got %v", fetched)
	}
}

// TestAsuraComicSearchReportsATimeoutInsteadOfNoMatches pins that a search
// whose budget ran out before anything was read returns the deadline as an
// error, so the form says the search failed instead of "No matches found".
func TestAsuraComicSearchReportsATimeoutInsteadOfNoMatches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/comics/slow-series-7f873ca6">Slow Series</a><a href="/comics/slow-series-two-12345678">Slow Series Two</a></body></html>`))
	})
	mux.HandleFunc("/comics/", func(_ http.ResponseWriter, r *http.Request) {
		// Held until the client gives up, with a cap so a missed cancellation
		// cannot hang the test.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	results, err := conn.SearchByTitle(ctx, "slow series", 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the deadline as the error, got results %+v and error %v", results, err)
	}
}

// TestAsuraComicHealthCheckRequiresAParsedSeries pins that a browse page which
// loads but carries no series link (a redesign) fails the health check, so the
// connector shows as broken instead of healthy while every resolve comes back
// empty.
func TestAsuraComicHealthCheckRequiresAParsedSeries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><div class="new-layout">Nano Machine</div></body></html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	if err := conn.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected a browse page with no series link to fail the health check")
	}
}

// TestAsuraComicChapterlessSeriesPageFailsResolveButNotSearch pins both sides
// of the no-chapters verdict: the poll path reports the page as a failed
// resolve (so the poller logs it and asks the tracker's other sources), while
// search still lists the series, which may simply have no chapter out yet.
func TestAsuraComicChapterlessSeriesPageFailsResolveButNotSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><a href="/comics/new-series-7f873ca6">New Series</a></body></html>`))
	})
	mux.HandleFunc("/comics/new-series-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="New Series - Asura Scans">
</head>
<body>
  <h1>New Series</h1>
  <div>Coming soon</div>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/new-series-7f873ca6")
	if !errors.Is(err, connectors.ErrNoChaptersListed) {
		t.Fatalf("expected the no-chapters verdict, got result %v and error %v", resolved, err)
	}
	if resolved != nil {
		t.Fatalf("a failed resolve must not hand back a result, got %+v", resolved)
	}

	results, err := conn.SearchByTitle(context.Background(), "new series", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceItemID != "new-series-7f873ca6" {
		t.Fatalf("expected the chapterless series to stay in the results, got %+v", results)
	}
	if results[0].Title != "New Series" || results[0].LatestChapter != nil {
		t.Fatalf("expected title New Series with no chapter, got %q / %v", results[0].Title, results[0].LatestChapter)
	}
}
