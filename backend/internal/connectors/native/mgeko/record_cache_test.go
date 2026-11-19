package mgeko

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// mgekoSite is a two-page Mgeko: the manga page and the all-chapters page of
// one series, each counting its requests and each able to be made to fail.
type mgekoSite struct {
	pageHits, chapterHits atomic.Int32
	chapter               atomic.Int32
	// pageStatus and chaptersStatus answer that status instead of the page
	// when set.
	pageStatus, chaptersStatus atomic.Int32
	// pageChapter is a chapter the manga page itself links, 0 for none.
	pageChapter atomic.Int32
}

func newMgekoSite(t *testing.T) (*mgekoSite, *Connector) {
	t.Helper()
	site := &mgekoSite{}
	site.chapter.Store(118)
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/sample-series/", func(w http.ResponseWriter, _ *http.Request) {
		site.pageHits.Add(1)
		if status := site.pageStatus.Load(); status != 0 {
			http.Error(w, "unavailable", int(status))
			return
		}
		page := mgekoMetadataMangaPage
		if linked := site.pageChapter.Load(); linked != 0 {
			page += `<a href="/reader/en/sample-series-chapter-` + strconv.Itoa(int(linked)) + `-eng-li/"><time datetime="Nov. 2, 2026, 8:00 p.m.">5 days</time></a>`
		}
		_, _ = w.Write([]byte(page))
	})
	mux.HandleFunc("/manga/sample-series/all-chapters/", func(w http.ResponseWriter, _ *http.Request) {
		site.chapterHits.Add(1)
		if status := site.chaptersStatus.Load(); status != 0 {
			http.Error(w, "unavailable", int(status))
			return
		}
		number := strconv.Itoa(int(site.chapter.Load()))
		_, _ = w.Write([]byte(`<li><a href="/reader/en/sample-series-chapter-` + number + `-eng-li/" title="Chapter ` + number + `">
			<time class="chapter-update" datetime="Nov. 1, 2026, 6:00 p.m.">6 days</time></a></li>`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return site, NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
}

func (s *mgekoSite) requests() (page int32, chapters int32) {
	return s.pageHits.Load(), s.chapterHits.Load()
}

const mgekoSampleURL = "https://www.mgeko.cc/manga/sample-series/"

// A resolve reads the manga page once a day and the all-chapters page every
// time: the page describes the series, the listing is what moves between
// polls. Both used to be read on every poll.
func TestMgekoResolveCachesTheMangaPage(t *testing.T) {
	site, conn := newMgekoSite(t)

	for poll := 0; poll < 3; poll++ {
		if poll == 2 {
			site.chapter.Store(119)
		}
		resolved, err := conn.ResolveByURL(context.Background(), mgekoSampleURL)
		if err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		if resolved.Title != "Tonari no Seki no Yatsu ga Souiu Me de Mitekuru" || resolved.Metadata == nil ||
			len(resolved.Metadata.Genres) != 3 || resolved.Metadata.Genres[0] != "Comedy" {
			t.Fatalf("poll %d: the cached record must still give title and metadata, got %+v (%+v)", poll, resolved, resolved.Metadata)
		}
		if want := 118.0 + float64(poll/2); resolved.LatestChapter == nil || *resolved.LatestChapter != want {
			t.Fatalf("poll %d: chapter %v, want %v", poll, resolved.LatestChapter, want)
		}
		// What a caller does to its result stays with that result: the record
		// behind it answers every later resolve.
		resolved.Metadata.Genres[0] = "Edited"
		resolved.Metadata.Rating = nil
	}
	if page, chapters := site.requests(); page != 1 || chapters != 3 {
		t.Fatalf("manga page asked %d times, all-chapters %d; want 1 and 3", page, chapters)
	}
}

// With the manga page's record cached, a failed all-chapters page still falls
// back to the chapters the manga page links — which means reading that page
// then, since the cached record does not carry them. And when the page cannot
// be read either, the verdict is the listing's own error, not the page's.
func TestMgekoCachedRecordStillFallsBackToTheMangaPage(t *testing.T) {
	site, conn := newMgekoSite(t)

	if _, err := conn.ResolveByURL(context.Background(), mgekoSampleURL); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	site.chaptersStatus.Store(http.StatusServiceUnavailable)
	site.pageChapter.Store(117)
	resolved, err := conn.ResolveByURL(context.Background(), mgekoSampleURL)
	if err != nil {
		t.Fatalf("expected the manga page's chapters to stand in, got %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 117 {
		t.Fatalf("expected chapter 117 from the manga page, got %v", resolved.LatestChapter)
	}
	if page, chapters := site.requests(); page != 2 || chapters != 2 {
		t.Fatalf("manga page asked %d times, all-chapters %d; want 2 and 2", page, chapters)
	}

	site.pageStatus.Store(http.StatusBadGateway)
	if _, err := conn.ResolveByURL(context.Background(), mgekoSampleURL); !connectors.IsHTTPStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("expected the all-chapters 503 as the verdict, got %v", err)
	}

	// The site back, the record that outlived the failure serves again.
	site.pageStatus.Store(0)
	site.chaptersStatus.Store(0)
	before, _ := site.requests()
	if _, err := conn.ResolveByURL(context.Background(), mgekoSampleURL); err != nil {
		t.Fatalf("resolve after the outage: %v", err)
	}
	if after, _ := site.requests(); after != before {
		t.Fatalf("the manga page was read again (%d -> %d) although its record was cached", before, after)
	}
}

// The cover is on the manga page, so asking for it alone (the cover resolver,
// through connectors.CoverURLResolver) reads no all-chapters page, and nothing
// at all while the page's record is cached.
func TestMgekoCoverComesFromTheMangaPageAlone(t *testing.T) {
	mux := http.NewServeMux()
	var pageHits, chapterHits atomic.Int32
	mux.HandleFunc("/manga/sample-series/", func(w http.ResponseWriter, _ *http.Request) {
		pageHits.Add(1)
		_, _ = w.Write([]byte(`<html><head><meta property="og:image" content="/media/manga_covers/sample-series.jpg"></head>
			<body><h1 class="novel-title">Sample Series</h1></body></html>`))
	})
	mux.HandleFunc("/manga/sample-series/all-chapters/", func(w http.ResponseWriter, _ *http.Request) {
		chapterHits.Add(1)
		_, _ = w.Write([]byte(mgekoMetadataAllChaptersPage))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	conn := NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
	var resolver connectors.CoverURLResolver = conn

	const wantCover = "https://www.mgeko.cc/media/manga_covers/sample-series.jpg"
	for attempt := 0; attempt < 2; attempt++ {
		cover, err := resolver.ResolveCoverURL(context.Background(), mgekoSampleURL)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if cover != wantCover {
			t.Fatalf("attempt %d: cover %q, want %q", attempt, cover, wantCover)
		}
	}
	if pageHits.Load() != 1 || chapterHits.Load() != 0 {
		t.Fatalf("manga page asked %d times, all-chapters %d; want 1 and 0", pageHits.Load(), chapterHits.Load())
	}

	resolved, err := conn.ResolveByURL(context.Background(), mgekoSampleURL)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CoverImageURL != wantCover || pageHits.Load() != 1 || chapterHits.Load() != 1 {
		t.Fatalf("resolve reported %q after %d page and %d all-chapters requests; want the same cover after 1 and 1",
			resolved.CoverImageURL, pageHits.Load(), chapterHits.Load())
	}
	if _, err := resolver.ResolveCoverURL(context.Background(), "https://comick.dev/comic/x"); err == nil {
		t.Fatal("expected another site's URL to be refused")
	}
}

// TestMgekoDatetimeSpellings pins the date spellings the all-chapters page
// uses, through the replacer and the layouts that are now built once for the
// package instead of once per chapter.
func TestMgekoDatetimeSpellings(t *testing.T) {
	cases := map[string]time.Time{
		"Feb. 21, 2026, 6:00 p.m.":  time.Date(2026, 2, 21, 18, 0, 0, 0, time.UTC),
		"Sept. 3, 2026, 9 a.m.":     time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC),
		"March 4, 2026, 11:30 P.M.": time.Date(2026, 3, 4, 23, 30, 0, 0, time.UTC),
		"  Dec. 31, 2025  ":         time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
		"aug. 9, 2026 7:05 a.m.":    time.Date(2026, 8, 9, 7, 5, 0, 0, time.UTC),
		// With the no-break spaces the site's markup sometimes carries.
		"Jan.\u00a04,\u00a02026,\u00a08 p.m": time.Date(2026, 1, 4, 20, 0, 0, 0, time.UTC),
	}
	for raw, want := range cases {
		got := parseMgekoDatetime(raw)
		if got == nil || !got.Equal(want) {
			t.Errorf("%q parsed to %v, want %v", raw, got, want)
		}
	}
	for _, raw := range []string{"", "5 days, 23 hours", "2026-02-21"} {
		if got := parseMgekoDatetime(raw); got != nil {
			t.Errorf("%q parsed to %v, want no date", raw, got)
		}
	}
}
