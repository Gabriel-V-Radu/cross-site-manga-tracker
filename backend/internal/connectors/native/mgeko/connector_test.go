package mgeko

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMgekoConnectorResolveSearchAndChapterURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse-comics/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
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
