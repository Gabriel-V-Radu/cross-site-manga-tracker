package mgeko

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func TestMgekoConnectorResolveSearchAndChapterURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <ul class="novel-list grid col col2">
    <li class="novel-item">
      <a href="/manga/the-100-girlfriends-who-really-really-really-really-really-love-you/" title="The 100 Girlfriends Who Really, Really, Really, Really, Really Love You">
        <img class="lazy" src="/static/img/loading.gif" data-src="/media/manga_covers/the-100-girlfriends-who-really-really-really-really-really-love-you.jpg" alt="The 100 Girlfriends Who Really, Really, Really, Really, Really Love You" />
        <h4 class="novel-title text2row">The 100 Girlfriends Who Really, Really, Really, Really, Really Love You</h4>
        <div class="novel-stats">
          <strong> Chapters 244-eng-li</strong>
          <span><i class="fas fa-clock"></i> 5 days, 23 hours Ago</span>
        </div>
      </a>
    </li>
    <li class="novel-item">
      <a href="/manga/another-series/" title="Another Series">
        <img class="lazy" src="/media/manga_covers/another-series.jpg" alt="Another Series" />
        <h4 class="novel-title text2row">Another Series</h4>
        <div class="novel-stats">
          <strong> Chapters 10-eng-li</strong>
          <span><i class="fas fa-clock"></i> 1 week Ago</span>
        </div>
      </a>
    </li>
  </ul>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/the-100-girlfriends-who-really-really-really-really-really-love-you/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:image" content="https://www.mgeko.cc/media/manga_covers/the-100-girlfriends-who-really-really-really-really-really-love-you.jpg">
</head>
<body>
  <h1 class="novel-title">The 100 Girlfriends Who Really, Really, Really, Really, Really Love You</h1>
  <h2 class="alternative-title text1row">
    100 Kanojo, The 100 Girlfriends Who Really, Really, Really, Really, Really Love You, ???????
  </h2>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/the-100-girlfriends-who-really-really-really-really-really-love-you/all-chapters/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <li data-chapterno="1">
    <a href="/reader/en/the-100-girlfriends-who-really-really-really-really-really-love-you-chapter-244-eng-li/" title="Chapter 244">
      <strong class="chapter-title">244-eng-li</strong>
      <time class="chapter-update" datetime="Feb. 21, 2026, 6:00 p.m.">5 days, 23 hours</time>
    </a>
  </li>
  <li data-chapterno="1">
    <a href="/reader/en/the-100-girlfriends-who-really-really-really-really-really-love-you-chapter-243-eng-li/" title="Chapter 243">
      <strong class="chapter-title">243-eng-li</strong>
      <time class="chapter-update" datetime="Feb. 14, 2026, 2:45 p.m.">2 weeks</time>
    </a>
  </li>
  <li data-chapterno="1">
    <a href="/reader/en/the-100-girlfriends-who-really-really-really-really-really-love-you-chapter-240-2-eng-li/" title="Chapter 240-2">
      <strong class="chapter-title">240-2-eng-li</strong>
      <time class="chapter-update" datetime="Jan. 20, 2026, 9:30 p.m.">1 month</time>
    </a>
  </li>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})

	if err := conn.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := conn.ResolveByURL(context.Background(), "https://www.mgeko.cc/manga/the-100-girlfriends-who-really-really-really-really-really-love-you/")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "the-100-girlfriends-who-really-really-really-really-really-love-you" {
		t.Fatalf("unexpected source item id: %s", resolved.SourceItemID)
	}
	if resolved.Title != "The 100 Girlfriends Who Really, Really, Really, Really, Really Love You" {
		t.Fatalf("unexpected title: %s", resolved.Title)
	}
	if resolved.CoverImageURL != "https://www.mgeko.cc/media/manga_covers/the-100-girlfriends-who-really-really-really-really-really-love-you.jpg" {
		t.Fatalf("unexpected cover image: %s", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 244 {
		t.Fatalf("expected latest chapter 244, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected latest release date")
	}
	if resolved.LastUpdatedAt.Format("2006-01-02 15:04") != "2026-02-21 18:00" {
		t.Fatalf("unexpected latest release date: %s", resolved.LastUpdatedAt.Format("2006-01-02 15:04"))
	}
	if len(resolved.RelatedTitles) == 0 {
		t.Fatalf("expected related titles")
	}
	contains := func(values []string, target string) bool {
		for _, value := range values {
			if value == target {
				return true
			}
		}
		return false
	}
	if !contains(resolved.RelatedTitles, "100 Kanojo") {
		t.Fatalf("expected related titles to include 100 Kanojo, got %v", resolved.RelatedTitles)
	}
	if contains(resolved.RelatedTitles, resolved.Title) {
		t.Fatalf("did not expect primary title in related titles: %v", resolved.RelatedTitles)
	}

	results, err := conn.SearchByTitle(context.Background(), "girlfriends really really", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].SourceItemID != "the-100-girlfriends-who-really-really-really-really-really-love-you" {
		t.Fatalf("unexpected search source id: %s", results[0].SourceItemID)
	}
	if results[0].URL != "https://www.mgeko.cc/manga/the-100-girlfriends-who-really-really-really-really-really-love-you/" {
		t.Fatalf("unexpected search url: %s", results[0].URL)
	}
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 244 {
		t.Fatalf("expected search latest chapter 244, got %v", results[0].LatestChapter)
	}
	if results[0].LastUpdatedAt == nil {
		t.Fatalf("expected search latest release date from relative timestamp")
	}

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://www.mgeko.cc/manga/the-100-girlfriends-who-really-really-really-really-really-love-you/", 243)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if chapterURL != "https://www.mgeko.cc/reader/en/the-100-girlfriends-who-really-really-really-really-really-love-you-chapter-243-eng-li/" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}
}

func TestMgekoConnectorRejectsNonMgekoURL(t *testing.T) {
	conn := NewConnectorWithOptions("https://www.mgeko.cc", []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := conn.ResolveByURL(context.Background(), "https://example.com/manga/the-100-girlfriends/"); err == nil {
		t.Fatalf("expected non-mgeko url to fail")
	}
}

func TestMgekoConnectorResolveChapterURLSupportsDecimalChapter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/sample-series/all-chapters/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <li data-chapterno="1">
    <a href="/reader/en/sample-series-chapter-67-5-eng-li/">
      <strong class="chapter-title">67-5-eng-li</strong>
      <time class="chapter-update" datetime="Jan. 4, 2026, 8:00 p.m.">1 month</time>
    </a>
  </li>
  <li data-chapterno="1">
    <a href="/reader/en/sample-series-chapter-67-eng-li/">
      <strong class="chapter-title">67-eng-li</strong>
      <time class="chapter-update" datetime="Dec. 28, 2025, 8:00 p.m.">1 month</time>
    </a>
  </li>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://www.mgeko.cc/manga/sample-series/", 67.5)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if chapterURL != "https://www.mgeko.cc/reader/en/sample-series-chapter-67-5-eng-li/" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}
}

// mgekoBrowserPage is the script-rendered browse page the site serves at
// /browse-comics/ since its rebuild, cut from the live page (2026-11-08): an
// empty shell whose series arrive later from /browse-comics/data/, with no
// novel-item row in the HTML.
const mgekoBrowserPage = `<!DOCTYPE html>
<html lang="en">
<head><title>Mgeko Browser</title></head>
<body>
<article x-data="search_results" class="container" id="search-section" style="display: none;">
<form id="novelSearchForm" method="get" action="/search/">
<input x-model.debounce.1000ms="key" id="inputContent" name="inputContent" type="search" class="form-control" placeholder="Search By Manga Name" aria-label="Manga Search">
</form>
<section x-ref="searchres" id="novelListBase"></section>
</article>
<article id="explore" class="container">
<span id="total-comics">0 Comics</span>
<form class="browse-filters" method="GET" action="" id="browse-filters-form"></form>
<div id="comics-grid"></div>
</article>
</body>
</html>`

// mgekoSearchPage is the search page as the site still renders it, cut from
// the live page for "the" (2026-11-08); the relative times carry the site's
// non-breaking spaces.
const mgekoSearchPage = `<!DOCTYPE html>
<html lang="en">
<head><title>MangaRaw Browser</title></head>
<body>
<header id="Result">
<h1>Search Manga.</h1>
<p class="description">
Showing Results for the.
</p>
<nav class="paging">
<a href="?search=the&results=2">Next &#8594;</a>
</nav>
</header>
<section>
<ul class="novel-list grid col col2">

<li class="novel-item">
<a href="/manga/xkmv-blade-of-the-discarded/" title="Blade of the Discarded">
<div class="cover-wrap">
<figure class="novel-cover">

<img class="lazy" src="/static/img/loading.gif" data-src='/media/manga_covers/xkmv-blade-of-the-discarded.jpg' alt="Blade of the Discarded" />

</figure>
</div>
<h4 class="novel-title text2row">
Blade of the Discarded
</h4>
<h6 class="text1row">Author(S): updating</h6>
<div class="novel-stats">
<strong> Chapters 7-eng-li</strong>
<span><i class="fas fa-clock"></i> 3` + " " + `days, 7` + " " + `hours Ago</span>
</div>
</a>
</li>

<li class="novel-item">
<a href="/manga/solo-leveling-mg1/" title="Solo Leveling">
<div class="cover-wrap">
<figure class="novel-cover">

<img class="lazy" src="/static/img/loading.gif" data-src='/media/manga_covers/k722631932_2.jpg' alt="Solo Leveling" />

</figure>
</div>
<h4 class="novel-title text2row">
Solo Leveling
</h4>
<h6 class="text1row">Author(S): Sung-Lak Jang</h6>
<div class="novel-stats">
<strong> Chapters 200.5-eng-li</strong>
<span><i class="fas fa-clock"></i> 2` + " " + `years, 3` + " " + `months Ago</span>
</div>
</a>
</li>

</ul>
</section>
</body>
</html>`

// TestMgekoHealthCheckReadsTheSearchPage pins the health check on the page
// search reads: with the browse page now a script-rendered shell, a check that
// still read it reported a working connector broken.
func TestMgekoHealthCheckReadsTheSearchPage(t *testing.T) {
	var searched []string
	mux := http.NewServeMux()
	mux.HandleFunc("/browse-comics/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mgekoBrowserPage))
	})
	mux.HandleFunc("/search/", func(w http.ResponseWriter, r *http.Request) {
		searched = append(searched, r.URL.Query().Get("search"))
		_, _ = w.Write([]byte(mgekoSearchPage))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
	if err := conn.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if len(searched) != 1 || searched[0] != healthCheckQuery {
		t.Fatalf("expected one search for %q, got %q", healthCheckQuery, searched)
	}
	if entries := parseSearchEntries(mgekoBrowserPage, time.Now().UTC()); len(entries) != 0 {
		t.Fatalf("the browse shell should hold no series, parsed %d", len(entries))
	}
}

// TestMgekoHealthCheckRequiresAParsedSeries pins that a search page which
// loads without a single series row search can parse — the browse shell's
// fate, should search be rebuilt the same way — fails the health check
// instead of reporting the connector healthy.
func TestMgekoHealthCheckRequiresAParsedSeries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mgekoBrowserPage))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
	if err := conn.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected a search page with no series row to fail the health check")
	}
}

// TestMgekoSearchReadsAHalfChapter pins the search row's dotted half chapter
// ("Chapters 200.5-eng-li"), which used to come back as 200, alongside the
// whole-numbered rows and the relative times with non-breaking spaces.
func TestMgekoSearchReadsAHalfChapter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mgekoSearchPage))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
	results, err := conn.SearchByTitle(context.Background(), "solo leveling", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	got := results[0]
	if got.SourceItemID != "solo-leveling-mg1" || got.URL != "https://www.mgeko.cc/manga/solo-leveling-mg1/" {
		t.Fatalf("unexpected hit %q at %s", got.SourceItemID, got.URL)
	}
	if got.LatestChapter == nil || *got.LatestChapter != 200.5 {
		t.Fatalf("expected latest chapter 200.5, got %v", got.LatestChapter)
	}
	if got.CoverImageURL != "https://www.mgeko.cc/media/manga_covers/k722631932_2.jpg" {
		t.Fatalf("unexpected cover %s", got.CoverImageURL)
	}
	if got.LastUpdatedAt == nil || time.Since(*got.LastUpdatedAt) < 365*24*time.Hour {
		t.Fatalf("expected a release about two years back, got %v", got.LastUpdatedAt)
	}

	for raw, want := range map[string]float64{"200.5-eng-li": 200.5, "240-2-eng-li": 240.2, "7-eng-li": 7} {
		if parsed := parseMgekoChapterToken(raw); parsed == nil || *parsed != want {
			t.Errorf("parseMgekoChapterToken(%q) = %v, want %v", raw, parsed, want)
		}
	}
}

// mgekoMangaPage is a manga page as the site serves it: title and cover, but
// no /reader/en/ chapter links, so it cannot stand in for the all-chapters page.
const mgekoMangaPage = `
<!DOCTYPE html>
<html>
<body>
  <h1 class="novel-title">Sample Series</h1>
</body>
</html>`

// TestMgekoResolveFailsWhenNoChapterCouldBeRead pins that an all-chapters page
// which failed, or parsed to nothing, is a failed resolve when the manga page
// has no chapter links to fall back on. It used to come back as a success with
// no chapter: the poller stamped the tracker checked, logged nothing and never
// asked its other sources.
func TestMgekoResolveFailsWhenNoChapterCouldBeRead(t *testing.T) {
	cases := []struct {
		name        string
		allChapters http.HandlerFunc
		check       func(t *testing.T, err error)
	}{
		{
			name: "all-chapters request fails",
			allChapters: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			},
			check: func(t *testing.T, err error) {
				if !connectors.IsHTTPStatus(err, http.StatusServiceUnavailable) {
					t.Fatalf("expected the 503 to be returned, got %v", err)
				}
			},
		},
		{
			name: "all-chapters page parses to nothing",
			allChapters: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><div class="chapters-moved"></div></body></html>`))
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, connectors.ErrNoChaptersListed) {
					t.Fatalf("expected the no-chapters verdict, got %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/manga/sample-series/", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(mgekoMangaPage))
			})
			mux.HandleFunc("/manga/sample-series/all-chapters/", tc.allChapters)

			server := httptest.NewServer(mux)
			defer server.Close()

			conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
			resolved, err := conn.ResolveByURL(context.Background(), "https://www.mgeko.cc/manga/sample-series/")
			if err == nil {
				t.Fatalf("expected a failed resolve, got %+v", resolved)
			}
			if resolved != nil {
				t.Fatalf("a failed resolve must not hand back a result, got %+v", resolved)
			}
			tc.check(t, err)
		})
	}
}

// TestMgekoResolveFallsBackToMangaPageChapters pins the fallback that stays: a
// failed all-chapters page is not an error while the manga page itself links
// chapters to read the latest from.
func TestMgekoResolveFallsBackToMangaPageChapters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/sample-series/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <h1 class="novel-title">Sample Series</h1>
  <a href="/reader/en/sample-series-chapter-12-eng-li/"><time datetime="Jan. 4, 2026, 8:00 p.m.">1 month</time></a>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/sample-series/all-chapters/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://www.mgeko.cc/manga/sample-series/")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 12 {
		t.Fatalf("expected latest chapter 12 from the manga page, got %v", resolved.LatestChapter)
	}
}
