package webtoons

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

func TestWebtoonsConnectorSearchResolveAndChapterURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/en/search/immediate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
  "result": {
    "query": "Maybe Meant to Be",
    "searchedList": [
      {
        "titleNo": 4208,
        "title": "Maybe Meant to Be",
        "thumbnailMobile": "/20251108_267/176254899384398Kaq_JPEG/Thumb_Square_4208.jpg",
        "authorNameList": ["damcho","honeyskein"],
        "representGenre": "ROMANCE",
        "searchMode": "TITLE"
      },
      {
        "titleNo": 4208,
        "title": "damcho",
        "thumbnailMobile": "",
        "authorNameList": ["damcho"],
        "representGenre": "ROMANCE",
        "searchMode": "AUTHOR"
      }
    ]
  },
  "success": true
}`))
	})
	mux.HandleFunc("/episodeList", func(w http.ResponseWriter, r *http.Request) {
		titleNo := r.URL.Query().Get("titleNo")
		page := r.URL.Query().Get("page")
		if titleNo != "4208" {
			http.NotFound(w, r)
			return
		}

		if page == "2" {
			_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
	<ul id="_listUl">
		<li class="_episodeItem" data-episode-no="115">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-115/viewer?title_no=4208&episode_no=115"><span class="date">Dec 12, 2025</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="114">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-114/viewer?title_no=4208&episode_no=114"><span class="date">Dec 05, 2025</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="113">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-113/viewer?title_no=4208&episode_no=113"><span class="date">Nov 28, 2025</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="112">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-112/viewer?title_no=4208&episode_no=112"><span class="date">Nov 21, 2025</span></a>
		</li>
	</ul>
</body>
</html>`))
			return
		}

		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
	<link rel="canonical" href="https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208" />
	<meta property="og:title" content="Maybe Meant to Be" />
	<meta property="og:image" content="https://swebtoon-phinf.pstatic.net/20241128_42/173275051448173fPT_JPEG/cover.jpg?type=crop540_540" />
</head>
<body>
	<h1 class="subj">Maybe Meant to Be</h1>
	<ul id="_listUl">
		<li class="_episodeItem" data-episode-no="125">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-125/viewer?title_no=4208&episode_no=125"><span class="date">Feb 18, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="124">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-124/viewer?title_no=4208&episode_no=124"><span class="date">Feb 11, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="123">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-123/viewer?title_no=4208&episode_no=123"><span class="date">Feb 04, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="122">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-122/viewer?title_no=4208&episode_no=122"><span class="date">Jan 28, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="121">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-121/viewer?title_no=4208&episode_no=121"><span class="date">Jan 21, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="120">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-120/viewer?title_no=4208&episode_no=120"><span class="date">Jan 14, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="119">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-119/viewer?title_no=4208&episode_no=119"><span class="date">Jan 07, 2026</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="118">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-118/viewer?title_no=4208&episode_no=118"><span class="date">Dec 31, 2025</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="117">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-117/viewer?title_no=4208&episode_no=117"><span class="date">Dec 24, 2025</span></a>
		</li>
		<li class="_episodeItem" data-episode-no="116">
			<a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-116/viewer?title_no=4208&episode_no=116"><span class="date">Dec 17, 2025</span></a>
		</li>
	</ul>
	<div class="paginate">
		<a href="/en/romance/maybe-meant-to-be/list?title_no=4208&page=2"><span>2</span></a>
	</div>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})

	if err := connector.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	searchResults, err := connector.SearchByTitle(context.Background(), "Meant Maybe to Be", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(searchResults) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(searchResults))
	}
	if searchResults[0].SourceItemID != "4208" {
		t.Fatalf("expected source item id 4208, got %s", searchResults[0].SourceItemID)
	}
	if searchResults[0].Title != "Maybe Meant to Be" {
		t.Fatalf("expected search title Maybe Meant to Be, got %s", searchResults[0].Title)
	}
	expectedSearchURL := "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208"
	if searchResults[0].URL != expectedSearchURL {
		t.Fatalf("expected search url %s, got %s", expectedSearchURL, searchResults[0].URL)
	}
	expectedThumb := "https://swebtoon-phinf.pstatic.net/20241128_42/173275051448173fPT_JPEG/cover.jpg?type=crop540_540"
	if searchResults[0].CoverImageURL != expectedThumb {
		t.Fatalf("expected thumb %s, got %s", expectedThumb, searchResults[0].CoverImageURL)
	}
	if searchResults[0].LatestChapter == nil || *searchResults[0].LatestChapter != 125 {
		t.Fatalf("expected search latest chapter 125, got %v", searchResults[0].LatestChapter)
	}
	if searchResults[0].LastUpdatedAt == nil || searchResults[0].LastUpdatedAt.Format("2006-01-02") != "2026-02-18" {
		t.Fatalf("expected search latest release date 2026-02-18, got %v", searchResults[0].LastUpdatedAt)
	}

	resolved, err := connector.ResolveByURL(context.Background(), "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "4208" {
		t.Fatalf("expected resolved source item id 4208, got %s", resolved.SourceItemID)
	}
	if resolved.Title != "Maybe Meant to Be" {
		t.Fatalf("expected resolved title Maybe Meant to Be, got %s", resolved.Title)
	}
	if resolved.URL != "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208" {
		t.Fatalf("unexpected resolved url: %s", resolved.URL)
	}
	if resolved.CoverImageURL != "https://swebtoon-phinf.pstatic.net/20241128_42/173275051448173fPT_JPEG/cover.jpg?type=crop540_540" {
		t.Fatalf("unexpected resolved cover url: %s", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 125 {
		t.Fatalf("expected latest chapter 125, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || resolved.LastUpdatedAt.Format("2006-01-02") != "2026-02-18" {
		t.Fatalf("expected latest release date 2026-02-18, got %v", resolved.LastUpdatedAt)
	}

	chapterURL, err := connector.ResolveChapterURL(context.Background(), "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208", 112)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	expectedChapterURL := "https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-112/viewer?title_no=4208&episode_no=112"
	if chapterURL != expectedChapterURL {
		t.Fatalf("expected chapter url %s, got %s", expectedChapterURL, chapterURL)
	}
}

// TestWebtoonsEpisodeListWithoutEpisodesFailsResolveButNotSearch pins the
// pulled-title case: the episode list lands on the home page (200, no episode
// rows). The poll path must report a failed resolve rather than a successful
// check, while search keeps the hit built from the search API's own fields.
func TestWebtoonsEpisodeListWithoutEpisodesFailsResolveButNotSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/en/search/immediate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"searchedList":[{"titleNo":4208,"title":"Maybe Meant to Be","thumbnailMobile":"/thumb.jpg","searchMode":"TITLE"}]},"success":true}`))
	})
	mux.HandleFunc("/episodeList", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
	<link rel="canonical" href="https://www.webtoons.com/en/" />
	<meta property="og:title" content="WEBTOON" />
</head>
<body><div class="home">Popular series</div></body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208")
	if !errors.Is(err, connectors.ErrNoChaptersListed) {
		t.Fatalf("expected the no-chapters verdict, got result %v and error %v", resolved, err)
	}

	results, err := connector.SearchByTitle(context.Background(), "Maybe Meant to Be", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the search hit to survive, got %d results", len(results))
	}
	if results[0].Title != "Maybe Meant to Be" {
		t.Fatalf("expected the search API title, got %q", results[0].Title)
	}
	if results[0].URL != "https://www.webtoons.com/episodeList?titleNo=4208" {
		t.Fatalf("expected the canonical episode list URL, got %q", results[0].URL)
	}
	if results[0].LatestChapter != nil {
		t.Fatalf("expected no chapter, got %v", *results[0].LatestChapter)
	}
}

// TestWebtoonsResolveKeepsOnlyAURLNamingTheSameTitle pins that the resolved
// URL, which the poller stores over the tracker's, still names the title that
// was asked for. A canonical or redirect URL without that title_no would fail
// every later poll and chapter-link lookup, with nothing to restore it.
func TestWebtoonsResolveKeepsOnlyAURLNamingTheSameTitle(t *testing.T) {
	cases := []struct {
		name      string
		canonical string
		want      string
	}{
		{
			name:      "canonical naming the title",
			canonical: "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208",
			want:      "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208",
		},
		{
			name:      "canonical without a title number",
			canonical: "https://www.webtoons.com/en/",
			want:      "https://www.webtoons.com/episodeList?titleNo=4208",
		},
		{
			name:      "canonical naming another title",
			canonical: "https://www.webtoons.com/en/romance/other/list?title_no=9999",
			want:      "https://www.webtoons.com/episodeList?titleNo=4208",
		},
		{
			name:      "canonical on another site",
			canonical: "https://example.com/list?title_no=4208",
			want:      "https://www.webtoons.com/episodeList?titleNo=4208",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/episodeList", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `<html><head><link rel="canonical" href="%s" /><meta property="og:title" content="Maybe Meant to Be" /></head><body>
<li class="_episodeItem" data-episode-no="125"><a href="https://www.webtoons.com/en/romance/maybe-meant-to-be/ep-125/viewer?title_no=4208&episode_no=125"><span class="date">Feb 18, 2026</span></a></li>
</body></html>`, tc.canonical)
			})

			server := httptest.NewServer(mux)
			defer server.Close()

			connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
			resolved, err := connector.ResolveByURL(context.Background(), "https://www.webtoons.com/en/romance/maybe-meant-to-be/list?title_no=4208")
			if err != nil {
				t.Fatalf("resolve failed: %v", err)
			}
			if resolved.URL != tc.want {
				t.Fatalf("expected url %s, got %s", tc.want, resolved.URL)
			}
		})
	}
}

// gappedEpisodeListServer serves a title whose episodes run from latest down
// to 1, ten rows a page, with the numbers in missing taken down. Every page
// served is counted in fetches.
func gappedEpisodeListServer(latest int, missing map[int]bool, fetches *atomic.Int32) *httptest.Server {
	var episodes []int
	for number := latest; number >= 1; number-- {
		if !missing[number] {
			episodes = append(episodes, number)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/episodeList", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if raw := r.URL.Query().Get("page"); raw != "" {
			page, _ = strconv.Atoi(raw)
		}
		fetches.Add(1)

		var body strings.Builder
		body.WriteString(`<html><body><ul id="_listUl">`)
		for index := (page - 1) * 10; index < page*10 && index >= 0 && index < len(episodes); index++ {
			number := episodes[index]
			fmt.Fprintf(&body, `<li class="_episodeItem" data-episode-no="%d"><a href="https://www.webtoons.com/en/x/ep-%d/viewer?title_no=4208&episode_no=%d"><span class="date">Jan 1, 2026</span></a></li>`, number, number, number)
		}
		body.WriteString(`</ul></body></html>`)
		_, _ = w.Write([]byte(body.String()))
	})
	return httptest.NewServer(mux)
}

// TestWebtoonsResolveChapterURLStepsPastNumberingGaps pins the page search: a
// taken-down episode above the target moves it onto an earlier page than the
// ten-rows-a-page estimate, and the lookup steps toward it instead of
// answering "not carried" from the one estimated page.
func TestWebtoonsResolveChapterURLStepsPastNumberingGaps(t *testing.T) {
	var fetches atomic.Int32
	server := gappedEpisodeListServer(150, map[int]bool{140: true}, &fetches)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	titleURL := "https://www.webtoons.com/en/x/list?title_no=4208"

	chapterURL, err := connector.ResolveChapterURL(context.Background(), titleURL, 100)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if want := "https://www.webtoons.com/en/x/ep-100/viewer?title_no=4208&episode_no=100"; chapterURL != want {
		t.Fatalf("expected %s, got %s", want, chapterURL)
	}

	// The taken-down episode itself is not carried, and finding that out costs
	// page one and the estimated page 2 (139..130), whose step back lands on
	// page one again: no walk across the list.
	fetches.Store(0)
	_, err = connector.ResolveChapterURL(context.Background(), titleURL, 140)
	if !errors.Is(err, connectors.ErrChapterNotFound) {
		t.Fatalf("expected the typed not-found verdict for the missing episode, got %v", err)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("expected 2 page fetches, got %d", got)
	}
}

// TestWebtoonsResolveChapterURLReportsAFailedPageFetch pins that a page that
// could not be read comes back as that failure, not as the site saying it does
// not carry the episode.
func TestWebtoonsResolveChapterURLReportsAFailedPageFetch(t *testing.T) {
	var fetches atomic.Int32
	gapped := gappedEpisodeListServer(150, nil, &fetches)
	defer gapped.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		gapped.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	_, err := connector.ResolveChapterURL(context.Background(), "https://www.webtoons.com/en/x/list?title_no=4208", 100)
	if err == nil {
		t.Fatal("expected the failed page fetch to be reported")
	}
	if errors.Is(err, connectors.ErrChapterNotFound) {
		t.Fatalf("a failed page fetch must not read as not carried, got %v", err)
	}
	if !connectors.IsHTTPStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("expected the 503 to be carried, got %v", err)
	}
}

// TestWebtoonsApostropheTitleIsNotCutOff pins that a raw apostrophe inside a
// double-quoted og:title stays part of the title. Search replaces the search
// API's title with the page's, so a cut-off page title ("A Villain") used to
// overwrite a correct one.
func TestWebtoonsApostropheTitleIsNotCutOff(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/en/search/immediate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"searchedList":[{"titleNo":5000,"title":"A Villain's Will to Survive","thumbnailMobile":"/thumb.jpg","searchMode":"TITLE"}]},"success":true}`))
	})
	mux.HandleFunc("/episodeList", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head>
	<link rel="canonical" href="https://www.webtoons.com/en/fantasy/a-villains-will-to-survive/list?title_no=5000" />
	<meta property="og:title" content="A Villain's Will to Survive" />
	<meta property="og:image" content="https://swebtoon-phinf.pstatic.net/cover's.jpg" />
</head><body>
	<li class="_episodeItem" data-episode-no="49"><a href="https://www.webtoons.com/en/fantasy/a-villains-will-to-survive/ep-49/viewer?title_no=5000&episode_no=49"><span class="date">Mar 20, 2026</span></a></li>
</body></html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})

	resolved, err := connector.ResolveByURL(context.Background(), "https://www.webtoons.com/en/fantasy/a-villains-will-to-survive/list?title_no=5000")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Title != "A Villain's Will to Survive" {
		t.Fatalf("expected the full apostrophe title, got %q", resolved.Title)
	}
	if resolved.CoverImageURL != "https://swebtoon-phinf.pstatic.net/cover's.jpg" {
		t.Fatalf("expected the full cover URL, got %q", resolved.CoverImageURL)
	}

	results, err := connector.SearchByTitle(context.Background(), "villain's will", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Title != "A Villain's Will to Survive" {
		t.Fatalf("expected the full apostrophe title in search, got %+v", results)
	}
}

func TestWebtoonsConnectorRejectsNonWebtoonsURL(t *testing.T) {
	connector := NewConnectorWithOptions("https://www.webtoons.com", []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := connector.ResolveByURL(context.Background(), "https://example.com/en/romance/maybe-meant-to-be/list?title_no=4208"); err == nil {
		t.Fatalf("expected non-webtoons url to fail")
	}
}
