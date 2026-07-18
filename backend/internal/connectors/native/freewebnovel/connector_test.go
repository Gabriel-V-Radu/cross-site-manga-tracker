package freewebnovel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const searchResultsHTML = `
<!DOCTYPE html>
<html>
<body>
  <div class="ul-list1 ul-list1-2 ss-custom rank-list">
    <div class="li-row">
      <div class="li">
        <div class="con">
          <div class="pic">
            <a href="/novel/cultivation-online-novel">
              <img src="/files/article/image/1/1427/1427s.jpg" width="100" height="133" alt="Cultivation Online" title="Cultivation Online">
            </a>
          </div>
          <div class="txt">
            <h3 class="tit"><a href="/novel/cultivation-online-novel" title="Cultivation Online">Cultivation Online</a></h3>
            <div class="desc">
              <div class="item">
                <div class="right">
                  <a href="/novel/cultivation-online-novel/chapter-2540" class="chapter" title="Chapter 2540"><span class="s1">2540 Chapters</span></a>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
    <div class="li-row">
      <div class="li">
        <div class="con">
          <div class="pic">
            <a href="/novel/star-odyssey">
              <img src="https://freewebnovel.com/files/article/image/1/1698/1698s.jpg" alt="Star Odyssey" title="Star Odyssey">
            </a>
          </div>
          <div class="txt">
            <h3 class="tit"><a href="/novel/star-odyssey" title="Star Odyssey">Star Odyssey</a></h3>
            <div class="desc">
              <div class="item">
                <div class="right">
                  <a href="/novel/star-odyssey/chapter-4402" class="chapter" title="Chapter 4402"><span class="s1">4402 Chapters</span></a>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</body>
</html>`

const novelPageHTML = `
<!DOCTYPE html>
<html lang="en-US">
<head>
  <meta charset="UTF-8">
  <meta name="description" content="Read Star Odyssey novel...Alternative names: Step On The Star, Treading the Stars, ??.">
  <meta property="og:title" content="Star Odyssey">
  <meta property="og:image" content="https://freewebnovel.com/files/article/image/1/1698/1698s.jpg">
  <meta property="og:novel:category" content="Chinese Novel">
  <meta property="og:novel:novel_name" content="Star Odyssey">
  <meta property="og:novel:status" content="OnGoing">
  <meta property="og:novel:update_time" content="2026-07-19 03:20:10">
  <meta property="og:novel:lastest_chapter_name" content="Chapter 4402: Take A Look">
  <meta property="og:novel:lastest_chapter_url" content="https://freewebnovel.com/novel/star-odyssey/chapter-4402">
</head>
<body>
  <h1 class="tit">Star Odyssey</h1>
  <div class="txt">
    <div class="item">
      <span class="glyphicon glyphicon-tasks" title="Alternative names"></span>
      <div class="right">
        <span class="s1">Step On The Star, Treading the Stars, ??</span>
      </div>
    </div>
  </div>
</body>
</html>`

// apostropheNovelPageHTML mirrors the live site, which emits a raw apostrophe
// (not &#39;) inside double-quoted og: meta attributes.
const apostropheNovelPageHTML = `
<!DOCTYPE html>
<html lang="en-US">
<head>
  <meta property="og:title" content="Trash of the Count's Family">
  <meta property="og:image" content="https://freewebnovel.com/files/article/image/3/3021/3021s.jpg">
  <meta property="og:novel:novel_name" content="Trash of the Count's Family">
  <meta property="og:novel:update_time" content="2026-06-01 09:15:00">
  <meta property="og:novel:lastest_chapter_url" content="https://freewebnovel.com/novel/trash-of-the-counts-family/chapter-812">
</head>
<body>
  <h1 class="tit">Trash of the Count's Family</h1>
</body>
</html>`

func newTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(searchResultsHTML))
	})
	mux.HandleFunc("/novel/star-odyssey", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(novelPageHTML))
	})
	mux.HandleFunc("/novel/trash-of-the-counts-family", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(apostropheNovelPageHTML))
	})
	return httptest.NewServer(mux)
}

func TestFreeWebNovelResolveByURL(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := conn.ResolveByURL(context.Background(), "https://freewebnovel.com/novel/star-odyssey")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "star-odyssey" {
		t.Fatalf("unexpected source item id: %s", resolved.SourceItemID)
	}
	if resolved.Title != "Star Odyssey" {
		t.Fatalf("unexpected title: %s", resolved.Title)
	}
	if resolved.URL != "https://freewebnovel.com/novel/star-odyssey" {
		t.Fatalf("unexpected url: %s", resolved.URL)
	}
	if resolved.CoverImageURL != "https://freewebnovel.com/files/article/image/1/1698/1698s.jpg" {
		t.Fatalf("unexpected cover image: %s", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 4402 {
		t.Fatalf("expected latest chapter 4402, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected latest release date")
	}
	if got := resolved.LastUpdatedAt.Format("2006-01-02 15:04:05"); got != "2026-07-19 03:20:10" {
		t.Fatalf("unexpected latest release date: %s", got)
	}
	if !contains(resolved.RelatedTitles, "Step On The Star") || !contains(resolved.RelatedTitles, "Treading the Stars") {
		t.Fatalf("expected alternative names in related titles, got %v", resolved.RelatedTitles)
	}
	if contains(resolved.RelatedTitles, resolved.Title) {
		t.Fatalf("did not expect primary title in related titles: %v", resolved.RelatedTitles)
	}
}

func TestFreeWebNovelResolvePreservesApostropheTitle(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := conn.ResolveByURL(context.Background(), "https://freewebnovel.com/novel/trash-of-the-counts-family")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Title != "Trash of the Count's Family" {
		t.Fatalf("expected apostrophe title preserved, got %q", resolved.Title)
	}
	if contains(resolved.RelatedTitles, resolved.Title) {
		t.Fatalf("did not expect primary title to leak into related titles: %v", resolved.RelatedTitles)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 812 {
		t.Fatalf("expected latest chapter 812, got %v", resolved.LatestChapter)
	}
}

func TestFreeWebNovelSearchByTitle(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	results, err := conn.SearchByTitle(context.Background(), "star odyssey", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].SourceItemID != "star-odyssey" {
		t.Fatalf("unexpected search source id: %s", results[0].SourceItemID)
	}
	if results[0].URL != "https://freewebnovel.com/novel/star-odyssey" {
		t.Fatalf("unexpected search url: %s", results[0].URL)
	}
	if results[0].CoverImageURL != "https://freewebnovel.com/files/article/image/1/1698/1698s.jpg" {
		t.Fatalf("unexpected search cover: %s", results[0].CoverImageURL)
	}
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 4402 {
		t.Fatalf("expected search latest chapter 4402, got %v", results[0].LatestChapter)
	}
}

func TestFreeWebNovelSearchResolvesRelativeCover(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	results, err := conn.SearchByTitle(context.Background(), "cultivation online", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].SourceItemID != "cultivation-online-novel" {
		t.Fatalf("unexpected search source id: %s", results[0].SourceItemID)
	}
	if results[0].CoverImageURL != "https://freewebnovel.com/files/article/image/1/1427/1427s.jpg" {
		t.Fatalf("expected relative cover made absolute, got %s", results[0].CoverImageURL)
	}
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 2540 {
		t.Fatalf("expected search latest chapter 2540, got %v", results[0].LatestChapter)
	}
}

func TestFreeWebNovelResolveChapterURL(t *testing.T) {
	conn := NewConnectorWithOptions("https://freewebnovel.com", []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://freewebnovel.com/novel/star-odyssey", 4402)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if chapterURL != "https://freewebnovel.com/novel/star-odyssey/chapter-4402" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}

	// A chapter URL should still resolve the correct slug and rebuild the target chapter.
	chapterURL, err = conn.ResolveChapterURL(context.Background(), "https://freewebnovel.com/novel/star-odyssey/chapter-1", 100)
	if err != nil {
		t.Fatalf("resolve chapter url from chapter page failed: %v", err)
	}
	if chapterURL != "https://freewebnovel.com/novel/star-odyssey/chapter-100" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}
}

func TestFreeWebNovelResolveChapterURLRejectsInvalid(t *testing.T) {
	conn := NewConnectorWithOptions("https://freewebnovel.com", []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	if _, err := conn.ResolveChapterURL(context.Background(), "https://freewebnovel.com/novel/star-odyssey", 0); err == nil {
		t.Fatalf("expected invalid chapter to fail")
	}
	if _, err := conn.ResolveChapterURL(context.Background(), "https://example.com/novel/star-odyssey", 5); err == nil {
		t.Fatalf("expected non-freewebnovel url to fail")
	}
}

func TestFreeWebNovelRejectsNonFreeWebNovelURL(t *testing.T) {
	conn := NewConnectorWithOptions("https://freewebnovel.com", []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := conn.ResolveByURL(context.Background(), "https://example.com/novel/star-odyssey"); err == nil {
		t.Fatalf("expected non-freewebnovel url to fail")
	}
	if _, err := conn.ResolveByURL(context.Background(), "https://freewebnovel.com/genre/Action"); err == nil {
		t.Fatalf("expected non-novel url to fail")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
