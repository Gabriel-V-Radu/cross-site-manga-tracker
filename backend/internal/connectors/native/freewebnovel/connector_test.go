package freewebnovel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
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
  <meta property="og:description" content="Read Star Odyssey by Along With The Wind online for free. Join Lu Yin on an epic journey across the Universe, pursuing the truth and tragedy of his past. This is a world of science fantasy where...">
  <meta property="og:novel:category" content="Chinese Novel">
  <meta property="og:novel:genre" content="Action, Adventure, Harem, Sci-fi, Seinen, Xuanhuan">
  <meta property="og:novel:author" content="Along With The Wind,随散飘风">
  <meta property="og:novel:author_link" content="https://freewebnovel.com/author/Along With The Wind">
  <meta property="og:novel:novel_name" content="Star Odyssey">
  <meta property="og:novel:status" content="OnGoing">
  <meta property="og:novel:update_time" content="2026-07-19 03:20:10">
  <meta property="og:novel:lastest_chapter_name" content="Chapter 4402: Take A Look">
  <meta property="og:novel:lastest_chapter_url" content="https://freewebnovel.com/novel/star-odyssey/chapter-4402">
  <script type="application/ld+json">{"@context":"https://schema.org","@type":"Book","name":"Star Odyssey","author":[{"@type":"Person","name":"Along With The Wind"},{"@type":"Person","name":"随散飘风"}],"genre":["Action","Adventure","Harem","Sci-fi","Seinen","Xuanhuan"],"description":"Join Lu Yin on an epic journey across the Universe, pursuing the truth and tragedy of his past. This is a world of science fantasy where the older generations step back and allow the young to take charge of affairs. Dotting Lu Yin’s path are monumental feats of kingdo","aggregateRating":{"@type":"AggregateRating","ratingValue":4,"bestRating":5,"ratingCount":523}}</script>
  <script type="application/ld+json">{"@context":"https://schema.org","@type":"BreadcrumbList","itemListElement":[{"@type":"ListItem","position":1,"name":"Home","item":"https://freewebnovel.com/home"}]}</script>
</head>
<body>
  <div class="m-imgtxt">
    <div class="txt">
      <div class="item">
        <span class="glyphicon glyphicon-tasks" title="Alternative names"></span>
        <div class="right">
          <span class="s1">Step On The Star, Treading the Stars, ??</span>
        </div>
      </div>
      <div class="item">
        <span class="glyphicon glyphicon-time" title="Status"></span>
        <div class="right"><span class="s1 s2"><a href="/sort/latest-release" title="Latest Release Novels">OnGoing</a></span></div>
      </div>
    </div>
  </div>
  <div class="m-desc">
    <h1 class="tit">Star Odyssey</h1>
    <div class="score"><p class="vote">4.0 / 5 ( 523 votes )</p></div>
    <h4 class="abstract"><span class="glyphicon glyphicon-info-sign"></span> SUMMARY</h4>
    <div class="txt">
      <div class="inner">
      <p>Join Lu Yin on an epic journey across the Universe, pursuing the truth and tragedy of his past.</p><p>Dotting Lu Yin&rsquo;s path are monumental feats of kingdom-building and treacherous political situations.</p>        </div>
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

// noChapterMetaNovelPageHTML is a novel page that loads but no longer carries
// the og:novel:lastest_chapter_url meta the latest chapter is read from.
const noChapterMetaNovelPageHTML = `
<!DOCTYPE html>
<html lang="en-US">
<head>
  <meta property="og:title" content="Star Odyssey">
</head>
<body>
  <h1 class="tit">Star Odyssey</h1>
</body>
</html>`

func newTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h3 class="tit"><a href="/novel/star-odyssey" title="Star Odyssey">Star Odyssey</a></h3></body></html>`))
	})
	mux.HandleFunc("/novel/no-chapter-meta", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(noChapterMetaNovelPageHTML))
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
	// The fixture meta says "2026-07-19 03:20:10", which the site renders in
	// UTC+8; the connector must convert it to UTC.
	if got := resolved.LastUpdatedAt.Format("2006-01-02 15:04:05"); got != "2026-07-18 19:20:10" {
		t.Fatalf("unexpected latest release date: %s", got)
	}
	if !contains(resolved.RelatedTitles, "Step On The Star") || !contains(resolved.RelatedTitles, "Treading the Stars") {
		t.Fatalf("expected alternative names in related titles, got %v", resolved.RelatedTitles)
	}
	if contains(resolved.RelatedTitles, resolved.Title) {
		t.Fatalf("did not expect primary title in related titles: %v", resolved.RelatedTitles)
	}
}

// The novel page the resolve already fetches describes the work: og:novel:*
// metas, the schema.org Book block's rating and, in the body, the whole
// synopsis (the meta and JSON-LD copies are cut off mid-word).
func TestFreeWebNovelResolveReadsNovelMetadata(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := conn.ResolveByURL(context.Background(), "https://freewebnovel.com/novel/star-odyssey")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	metadata := resolved.Metadata
	if metadata == nil {
		t.Fatal("expected metadata")
	}
	if metadata.Type != connectors.SeriesTypeNovel {
		t.Fatalf("type = %q", metadata.Type)
	}
	if metadata.Status != connectors.SeriesStatusOngoing {
		t.Fatalf("status = %q", metadata.Status)
	}
	if got := strings.Join(metadata.Genres, "|"); got != "Action|Adventure|Harem|Sci-fi|Seinen|Xuanhuan" {
		t.Fatalf("genres = %v", metadata.Genres)
	}
	// The author_link meta must not be mistaken for the author.
	if got := strings.Join(metadata.Authors, "|"); got != "Along With The Wind|随散飘风" {
		t.Fatalf("authors = %v", metadata.Authors)
	}
	wantDescription := "Join Lu Yin on an epic journey across the Universe, pursuing the truth and tragedy of his past.\n\n" +
		"Dotting Lu Yin’s path are monumental feats of kingdom-building and treacherous political situations."
	if metadata.Description != wantDescription {
		t.Fatalf("description = %q", metadata.Description)
	}
	// 4 out of 5.
	if metadata.Rating == nil || *metadata.Rating != 8 {
		t.Fatalf("rating = %v", metadata.Rating)
	}
	if metadata.RatingVotes != 523 {
		t.Fatalf("rating votes = %d", metadata.RatingVotes)
	}
}

// A page with none of the metadata, or with it in odd shapes, resolves as
// before. The type is still known (everything here is a novel), the cut-off
// og:description stands in for a missing synopsis once its stock opening is
// stripped, and a JSON-LD block that does not decode is skipped for the next.
func TestFreeWebNovelOddOrMissingMetadataDoesNotFailTheResolve(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})

	bare, err := conn.ResolveByURL(context.Background(), "https://freewebnovel.com/novel/trash-of-the-counts-family")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if bare.Metadata == nil || bare.Metadata.Type != connectors.SeriesTypeNovel {
		t.Fatalf("expected the novel type alone, got %+v", bare.Metadata)
	}
	if bare.Metadata.Status != "" || bare.Metadata.Genres != nil || bare.Metadata.Authors != nil ||
		bare.Metadata.Description != "" || bare.Metadata.Rating != nil {
		t.Fatalf("expected nothing else from a page without metadata, got %+v", bare.Metadata)
	}

	odd := novelMetadata(`<html><head>
  <meta property="og:description" content="Read Odd Novel by Someone online for free. A short &amp; sweet tale that stops where...">
  <meta property="og:novel:status" content="Unknown">
  <meta property="og:novel:genre" content=" , Updating">
  <script type="application/ld+json">{"@type":"Book","aggregateRating":{"ratingValue":"N/A"}</script>
  <script type="application/ld+json">{"@type":"Book","aggregateRating":{"@type":"AggregateRating","ratingValue":"4.5","bestRating":"5","ratingCount":"12"}}</script>
</head><body></body></html>`)
	if odd.Description != "A short & sweet tale that stops where..." {
		t.Fatalf("description = %q", odd.Description)
	}
	if odd.Status != "" || odd.Genres != nil {
		t.Fatalf("expected no status and no genres, got %q and %v", odd.Status, odd.Genres)
	}
	if odd.Rating == nil || *odd.Rating != 9 || odd.RatingVotes != 12 {
		t.Fatalf("rating = %v (%d votes), want 9 from 4.5/5 with 12 votes", odd.Rating, odd.RatingVotes)
	}

	// A description that does not open with the stock sentence is some other
	// text, and an unrated novel has no score.
	other := novelMetadata(`<meta property="og:description" content="Read Star Odyssey novel...Alternative names: A, B.">
<script type="application/ld+json">{"aggregateRating":{"ratingValue":0,"bestRating":5,"ratingCount":0}}</script>`)
	if other.Description != "" || other.Rating != nil || other.RatingVotes != 0 {
		t.Fatalf("expected no description and no rating, got %+v", other)
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
	// Only a resolve reads the novel page, so only a resolve reports metadata.
	if results[0].Metadata != nil {
		t.Fatalf("expected no metadata on a search result, got %+v", results[0].Metadata)
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

// TestChapterNumbersUseTheNovelPlausibilityBound pins that the comic-derived
// ceiling is not applied here. Translated web novels run into the thousands —
// the fixture series in this file is already past 4000 — and a rejected number
// does not fail loudly: the poller simply has nothing to store, so the tracker
// stops advancing and looks abandoned rather than broken.
func TestChapterNumbersUseTheNovelPlausibilityBound(t *testing.T) {
	beyondComicBound := connectors.MaxPlausibleChapter + 2345.0
	parsed := parseChapterNumber(connectors.FormatChapter(beyondComicBound))
	if parsed == nil {
		t.Fatalf("chapter %v must parse: novels legitimately run past the comic bound", beyondComicBound)
	}
	if *parsed != beyondComicBound {
		t.Fatalf("parseChapterNumber = %v, want %v", *parsed, beyondComicBound)
	}

	if parsed := parseChapterNumber("0"); parsed != nil {
		t.Fatalf("chapter 0 must be rejected, got %v", *parsed)
	}
	if parsed := parseChapterNumber(connectors.FormatChapter(connectors.MaxPlausibleNovelChapter + 1)); parsed != nil {
		t.Fatalf("a number past the novel bound must be rejected as parser noise, got %v", *parsed)
	}
}

// TestResolveChapterURLReportsAnImpossibleChapterAsNotCarried pins the error
// classification: the reading chain only cedes a site's turn when it says it
// does not carry the chapter, so an unwrapped error would strand the reader on
// the bare series URL instead of trying the next site.
func TestResolveChapterURLReportsAnImpossibleChapterAsNotCarried(t *testing.T) {
	connector := NewConnector()

	_, err := connector.ResolveChapterURL(context.Background(), "https://freewebnovel.com/novel/star-odyssey", 0)
	if err == nil {
		t.Fatalf("expected chapter 0 to be refused")
	}
	if !errors.Is(err, connectors.ErrChapterNotFound) {
		t.Fatalf("error must wrap ErrChapterNotFound, got %v", err)
	}
}

// TestFreeWebNovelHealthCheckRequiresANovelLink pins that the health check
// reads the home page instead of trusting its status: a 200 page with no
// /novel/{slug} link (a challenge page, a redesign) reports the connector as
// broken.
func TestFreeWebNovelHealthCheckRequiresANovelLink(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})
	if err := conn.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed on a home page that links novels: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Just a moment...</body></html>`))
	})
	bare := httptest.NewServer(mux)
	defer bare.Close()

	conn = NewConnectorWithOptions(bare.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})
	if err := conn.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected a home page with no novel link to fail the health check")
	}
}

// TestFreeWebNovelResolveWithoutLatestChapterMetaFails pins that a novel page
// missing og:novel:lastest_chapter_url is a failed resolve, so the poller logs
// it and asks the tracker's other sources instead of stamping it as checked.
func TestFreeWebNovelResolveWithoutLatestChapterMetaFails(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"freewebnovel.com"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://freewebnovel.com/novel/no-chapter-meta")
	if !errors.Is(err, connectors.ErrNoChaptersListed) {
		t.Fatalf("expected the no-chapters verdict, got result %v and error %v", resolved, err)
	}
	if resolved != nil {
		t.Fatalf("a failed resolve must not hand back a result, got %+v", resolved)
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
