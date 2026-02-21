package mangafire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMangaFireConnector(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/manga/one-piecee.dkw"><img src="https://cdn.example/onepiece.jpg" alt="One Piece"></a>
  <a href="/manga/one-piecee.dkw">One Piece</a>
  <a href="/manga/blue-lockk.kw9j9"><img src="https://cdn.example/bluelock.jpg" alt="Blue Lock"></a>
  <a href="/manga/blue-lockk.kw9j9">Blue Lock</a>
</body>
</html>`))
	})
	mux.HandleFunc("/filter", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/manga/one-piecee.dkw"><img src="https://cdn.example/onepiece.jpg" alt="One Piece"></a>
  <a href="/manga/one-piecee.dkw">One Piece</a>
  <a href="/manga/one-punch-mann.oo4"><img src="https://cdn.example/onepunch.jpg" alt="One-Punch Man"></a>
  <a href="/manga/one-punch-mann.oo4">One-Punch Man</a>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/one-piecee.dkw", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="One Piece">
  <meta property="og:image" content="https://cdn.example/onepiece-full.jpg">
</head>
<body>
	<div>Alternative Names: Pirate Legacy | One Piece Treasure Cruise</div>
	<a href="/read/one-piecee.dkw/en/chapter-1172">Chapter 1172</a><span>Dec 22, 2025</span>
	<a href="/read/one-piecee.dkw/en/chapter-1173">Chapter 1173</a><span>Jan 12, 2026</span>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/one-punch-mann.oo4", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="One-Punch Man">
</head>
<body>
  <a href="/read/one-punch-mann.oo4/en/chapter-264">Chapter 264</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	if err := connector.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/manga/one-piecee.dkw")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "one-piecee.dkw" {
		t.Fatalf("expected source item id one-piecee.dkw, got %s", resolved.SourceItemID)
	}
	if resolved.Title != "One Piece" {
		t.Fatalf("expected title One Piece, got %s", resolved.Title)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 1173 {
		t.Fatalf("expected latest chapter 1173, got %v", resolved.LatestChapter)
	}
	if resolved.CoverImageURL != "https://cdn.example/onepiece-full.jpg" {
		t.Fatalf("unexpected cover image: %s", resolved.CoverImageURL)
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected release date to be scraped")
	}
	if resolved.LastUpdatedAt.Format("2006-01-02") != "2026-01-12" {
		t.Fatalf("expected release date 2026-01-12, got %s", resolved.LastUpdatedAt.Format("2006-01-02"))
	}
	if len(resolved.RelatedTitles) == 0 {
		t.Fatalf("expected related titles from alternative names")
	}
	for _, related := range resolved.RelatedTitles {
		if related == resolved.Title {
			t.Fatalf("did not expect primary title in related titles: %q", related)
		}
	}

	results, err := connector.SearchByTitle(context.Background(), "one", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, item := range results {
		switch item.SourceItemID {
		case "one-piecee.dkw":
			if item.LatestChapter == nil || *item.LatestChapter != 1173 {
				t.Fatalf("expected One Piece latest chapter 1173, got %v", item.LatestChapter)
			}
			if item.LastUpdatedAt == nil || item.LastUpdatedAt.Format("2006-01-02") != "2026-01-12" {
				t.Fatalf("expected One Piece release date 2026-01-12, got %v", item.LastUpdatedAt)
			}
		case "one-punch-mann.oo4":
			if item.LatestChapter == nil || *item.LatestChapter != 264 {
				t.Fatalf("expected One-Punch Man latest chapter 264, got %v", item.LatestChapter)
			}
		default:
			t.Fatalf("unexpected search source id: %s", item.SourceItemID)
		}
	}

	aliasResults, err := connector.SearchByTitle(context.Background(), "pirate legacy", 10)
	if err != nil {
		t.Fatalf("alias search failed: %v", err)
	}
	if len(aliasResults) != 1 {
		t.Fatalf("expected 1 alias result, got %d", len(aliasResults))
	}
	if aliasResults[0].SourceItemID != "one-piecee.dkw" {
		t.Fatalf("expected alias result source id one-piecee.dkw, got %s", aliasResults[0].SourceItemID)
	}
}

func TestMangaFireConnectorSitemapFallbackAndPosterCover(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><a href="/manga/narutoo.l33">Naruto</a></body></html>`))
	})
	mux.HandleFunc("/filter", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><a href="/manga/narutoo.l33">Naruto</a></body></html>`))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex><sitemap><loc>http://` + r.Host + `/sitemap-list-1.xml</loc></sitemap></sitemapindex>`))
	})
	mux.HandleFunc("/sitemap-list-1.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>http://` + r.Host + `/manga/bukiyou-na-senpaii.2nw2</loc></url></urlset>`))
	})
	mux.HandleFunc("/manga/bukiyou-na-senpaii.2nw2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="My Awkward Senpai Manga - Read Manga Online Free">
</head>
<body>
  <div class="poster"><div><img src="https://static.mfcdn.cc/d88c/i/6/9e/3a401038dc71b28eeb6b2a4e40e7a8c8.jpg" alt="My Awkward Senpai"></div></div>
  <a href="/read/bukiyou-na-senpaii.2nw2/en/chapter-105">Chapter 105</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	results, err := connector.SearchByTitle(context.Background(), "bukiyou na senpai", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least 1 result from sitemap fallback")
	}

	found := false
	for _, item := range results {
		if item.SourceItemID == "bukiyou-na-senpaii.2nw2" {
			found = true
			if item.Title != "My Awkward Senpai" {
				t.Fatalf("expected sanitized title My Awkward Senpai, got %s", item.Title)
			}
			if item.CoverImageURL == "" {
				t.Fatalf("expected cover image URL from poster image")
			}
			if item.LatestChapter == nil || *item.LatestChapter != 105 {
				t.Fatalf("expected latest chapter 105, got %v", item.LatestChapter)
			}
		}
	}

	if !found {
		t.Fatalf("expected result with source item id bukiyou-na-senpaii.2nw2")
	}
}

func TestMangaFireConnectorSearchHandlesNestedTitleAndStopWords(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/filter", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a class="unit" href="/manga/bukiyou-na-senpaii.2nw2"><img src="https://cdn.example/awkward.jpg"></a>
  <a class="unit" href="/manga/bukiyou-na-senpaii.2nw2"><h3>Awkward Senpai (Webcomic)</h3></a>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/bukiyou-na-senpaii.2nw2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="My Awkward Senpai Manga - Read Manga Online Free">
</head>
<body>
  <a href="/read/bukiyou-na-senpaii.2nw2/en/chapter-105">Chapter 105</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	results, err := connector.SearchByTitle(context.Background(), "My Awkward Senpai", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least one result")
	}

	if results[0].SourceItemID != "bukiyou-na-senpaii.2nw2" {
		t.Fatalf("expected source item id bukiyou-na-senpaii.2nw2, got %s", results[0].SourceItemID)
	}
	if results[0].Title != "My Awkward Senpai" {
		t.Fatalf("expected sanitized title My Awkward Senpai, got %s", results[0].Title)
	}
}

func TestMangaFireConnectorSearchReturnsNoErrorOnRateLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/filter", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	})
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex><sitemap><loc>http://` + r.Host + `/sitemap-list-1.xml</loc></sitemap></sitemapindex>`))
	})
	mux.HandleFunc("/sitemap-list-1.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>http://` + r.Host + `/manga/ordinary-senpaii.aaaa</loc></url><url><loc>http://` + r.Host + `/manga/bukiyou-na-senpaii.2nw2</loc></url></urlset>`))
	})
	mux.HandleFunc("/manga/ordinary-senpaii.aaaa", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Ordinary Senpai Manga - Read Manga Online Free">
</head>
<body>
  <a href="/read/ordinary-senpaii.aaaa/en/chapter-12">Chapter 12</a>
</body>
</html>`))
	})
	mux.HandleFunc("/manga/bukiyou-na-senpaii.2nw2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="My Awkward Senpai Manga - Read Manga Online Free">
</head>
<body>
  <a href="/read/bukiyou-na-senpaii.2nw2/en/chapter-105">Chapter 105</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	results, err := connector.SearchByTitle(context.Background(), "My Awkward Senpai", 8)
	if err != nil {
		t.Fatalf("expected no error on rate limit, got: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected fallback results on rate limit")
	}

	found := false
	for _, item := range results {
		if item.SourceItemID == "bukiyou-na-senpaii.2nw2" {
			found = true
			if item.Title != "My Awkward Senpai" {
				t.Fatalf("expected sanitized title My Awkward Senpai, got %s", item.Title)
			}
		}
	}
	if !found {
		t.Fatalf("expected My Awkward Senpai result under rate limit fallback")
	}
}

func TestMangaFireConnectorSitemapFallbackMatchesAliasQuery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/filter", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body></body></html>`))
	})
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body></body></html>`))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex><sitemap><loc>http://` + r.Host + `/sitemap-list-1.xml</loc></sitemap></sitemapindex>`))
	})
	mux.HandleFunc("/sitemap-list-1.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>http://` + r.Host + `/manga/bukiyou-na-senpaii.2nw2</loc></url></urlset>`))
	})
	mux.HandleFunc("/manga/bukiyou-na-senpaii.2nw2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="My Awkward Senpai Manga - Read Manga Online Free">
</head>
<body>
  <a href="/read/bukiyou-na-senpaii.2nw2/en/chapter-105">Chapter 105</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	results, err := connector.SearchByTitle(context.Background(), "My Awkward Senpai", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected sitemap fallback to return alias result")
	}

	found := false
	for _, item := range results {
		if item.SourceItemID == "bukiyou-na-senpaii.2nw2" {
			found = true
			if item.Title != "My Awkward Senpai" {
				t.Fatalf("expected sanitized title My Awkward Senpai, got %s", item.Title)
			}
		}
	}
	if !found {
		t.Fatalf("expected result with source item id bukiyou-na-senpaii.2nw2")
	}
}

func TestExtractLatestChapterAndReleaseAt_UsesRelativeDateFromLatestChapterRow(t *testing.T) {
	body := `
<!DOCTYPE html>
<html>
<body>
  <div class="list-body">
    <ul class="scroll-sm">
      <li class="item" data-number="341">
        <a href="/read/global-martial-artss.w1lm6/en/chapter-341" title=" Chap 341">
          <span>Chapter 341: </span>
          <span>31 minutes ago</span>
        </a>
      </li>
      <li class="item" data-number="340">
        <a href="/read/global-martial-artss.w1lm6/en/chapter-340" title=" Chap 340">
          <span>Chapter 340: </span>
          <span>Feb 11, 2026</span>
        </a>
      </li>
    </ul>
  </div>
</body>
</html>`

	before := time.Now().UTC().Add(-35 * time.Minute)
	latestChapter, latestReleaseAt := extractLatestChapterAndReleaseAt(body)
	after := time.Now().UTC().Add(-25 * time.Minute)

	if latestChapter == nil || *latestChapter != 341 {
		t.Fatalf("expected latest chapter 341, got %v", latestChapter)
	}
	if latestReleaseAt == nil {
		t.Fatalf("expected latest release date to be parsed")
	}
	if latestReleaseAt.Before(before) || latestReleaseAt.After(after) {
		t.Fatalf("expected relative date around 31 minutes ago, got %s", latestReleaseAt.Format(time.RFC3339))
	}
	if latestReleaseAt.Format("2006-01-02") == "2026-02-11" {
		t.Fatalf("expected latest release date not to use previous chapter absolute date")
	}
}

func TestExtractLatestChapterAndReleaseAt_DuplicateLatestChapterPicksNewestDate(t *testing.T) {
	body := `
<!DOCTYPE html>
<html>
<body>
	<div class="noise">
		<a href="/read/global-martial-artss.w1lm6/en/chapter-341">Mirror entry</a>
		<span>Feb 11, 2026</span>
	</div>

	<ul class="scroll-sm">
		<li class="item" data-number="341">
			<a href="/read/global-martial-artss.w1lm6/en/chapter-341" title=" Chap 341">
				<span>Chapter 341:</span>
				<span>31 minutes ago</span>
			</a>
		</li>
		<li class="item" data-number="340">
			<a href="/read/global-martial-artss.w1lm6/en/chapter-340" title=" Chap 340">
				<span>Chapter 340:</span>
				<span>Feb 11, 2026</span>
			</a>
		</li>
	</ul>
</body>
</html>`

	before := time.Now().UTC().Add(-35 * time.Minute)
	latestChapter, latestReleaseAt := extractLatestChapterAndReleaseAt(body)
	after := time.Now().UTC().Add(-25 * time.Minute)

	if latestChapter == nil || *latestChapter != 341 {
		t.Fatalf("expected latest chapter 341, got %v", latestChapter)
	}
	if latestReleaseAt == nil {
		t.Fatalf("expected latest release date to be parsed")
	}
	if latestReleaseAt.Before(before) || latestReleaseAt.After(after) {
		t.Fatalf("expected freshest date around 31 minutes ago, got %s", latestReleaseAt.Format(time.RFC3339))
	}
}

func TestBuildMangaFireRelatedTitlesExtractsInfoAliases(t *testing.T) {
	body := `
<!DOCTYPE html>
<html>
<body>
  <h1>The Devil Butler (Colored)</h1>
  <h6>Demonic Emperor; The Devil Butler; Mo Huang Da Guanjia; Magic Emperor; Magic emperor; Devil Butler; Как демон-император стал дворецким; The Servant Is the Demon King?!; 魔皇大管家; Mo Huang Da Guan Jia</h6>
</body>
</html>`

	related := buildMangaFireRelatedTitles(body, "The Devil Butler", "mo-huang-da-guanjiaa.ykp3x")
	normalized := map[string]bool{}
	for _, item := range related {
		normalized[item] = true
	}

	for _, expected := range []string{
		"Mo Huang Da Guanjia",
		"Magic Emperor",
		"The Servant Is the Demon King?!",
		"Mo Huang Da Guan Jia",
	} {
		if !normalized[expected] {
			t.Fatalf("expected related titles to include %q", expected)
		}
	}
	if normalized["The Devil Butler"] {
		t.Fatalf("did not expect primary title to be included")
	}

	if normalized["Как демон-император стал дворецким"] {
		t.Fatalf("did not expect non-English related title to be included")
	}
	if normalized["魔皇大管家"] {
		t.Fatalf("did not expect kanji related title to be included")
	}
}
