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
	<meta property="og:type" content="com-linewebtoon:webtoon" />
	<meta property="og:image" content="https://swebtoon-phinf.pstatic.net/20241128_42/173275051448173fPT_JPEG/cover.jpg?type=crop540_540" />
	<meta property="og:description" content="Yuna&rsquo;s blind date turns out to be the boy she couldn&#39;t stand in high school&hellip; and he hasn't changed a bit." />
	<meta property="com-linewebtoon:webtoon:author" content="honeyskein / damcho" />
</head>
<body>
	<div class="detail_header type_white">
		<div class="info">
			<h2 class="genre g_romance">Romance</h2>
			<h1 class="subj">Maybe Meant to Be</h1>
			<div class="author_area">honeyskein , damcho <button type="button" class="ico_info2 _btnAuthorInfo">author info</button></div>
		</div>
	</div>
	<div class="aside detail" id="_asideDetail">
		<p class="day_info"><span class="txt_ico_completed"></span>COMPLETED</p>
		<p class="summary">Yuna&rsquo;s blind date turns out to be the boy she couldn&#39;t stand in high school&hellip; and he hasn't changed a bit.</p>
	</div>
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
	<ul class="lst_type1">
		<li><p class="genre g_fantasy">Fantasy</p><p class="author">seokyeong / mangnanyong</p></li>
	</ul>
	<script type="text/javascript">
		var options = {
			logParam: {
				titleNo: 4208
				, title: 'Maybe Meant to Be'
				, webtoonType: 'WEBTOON'
				, representGenre: 'ROMANCE'
				, contentLang: 'en'
			},
		};
	</script>
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
	// Search enriches its hits from the same page a resolve reads, but only a
	// resolve reports metadata.
	if searchResults[0].Metadata != nil {
		t.Fatalf("expected no metadata on a search result, got %+v", searchResults[0].Metadata)
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
	metadata := resolved.Metadata
	if metadata == nil {
		t.Fatal("expected metadata from the episode-list page")
	}
	if metadata.Status != connectors.SeriesStatusCompleted {
		t.Fatalf("expected status completed, got %q", metadata.Status)
	}
	// The recommendation rail's genre and authors belong to other titles.
	if strings.Join(metadata.Genres, "|") != "Romance" {
		t.Fatalf("expected genres [Romance], got %v", metadata.Genres)
	}
	if strings.Join(metadata.Authors, "|") != "honeyskein|damcho" {
		t.Fatalf("expected authors [honeyskein damcho], got %v", metadata.Authors)
	}
	if metadata.Artists != nil || metadata.Type != "" {
		t.Fatalf("expected no artists and no type, got %v and %q", metadata.Artists, metadata.Type)
	}
	wantDescription := "Yuna’s blind date turns out to be the boy she couldn't stand in high school… and he hasn't changed a bit."
	if metadata.Description != wantDescription {
		t.Fatalf("expected description %q, got %q", wantDescription, metadata.Description)
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

// TestWebtoonsResolveChapterURLPagesTheSluggedList pins the paging against the
// live site's redirect: the bare episodeList URL lands on the slugged list and
// loses its page number on the way, so every later page used to come back as
// page one and an episode past it read as not carried. Pages are asked for on
// the address page one landed on.
func TestWebtoonsResolveChapterURLPagesTheSluggedList(t *testing.T) {
	var fetches atomic.Int32
	gapped := gappedEpisodeListServer(150, nil, &fetches)
	defer gapped.Close()

	var sluggedPages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/episodeList":
			http.Redirect(w, r, "/en/x/list?title_no="+r.URL.Query().Get("titleNo"), http.StatusMovedPermanently)
		case "/en/x/list":
			sluggedPages = append(sluggedPages, r.URL.Query().Get("page"))
			listed := r.Clone(r.Context())
			listed.URL.Path = "/episodeList"
			gapped.Config.Handler.ServeHTTP(w, listed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	chapterURL, err := connector.ResolveChapterURL(context.Background(), "https://www.webtoons.com/en/x/list?title_no=4208", 100)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if want := "https://www.webtoons.com/en/x/ep-100/viewer?title_no=4208&episode_no=100"; chapterURL != want {
		t.Fatalf("expected %s, got %s", want, chapterURL)
	}
	if strings.Join(sluggedPages, ",") != ",6" {
		t.Fatalf("expected page one then page 6 on the slugged list, got %q", sluggedPages)
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

// resolveMetadataTestPage serves one episode-list page made of head and body
// around a single episode row, and resolves it.
func resolveMetadataTestPage(t *testing.T, head string, body string) *connectors.MangaResult {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/episodeList", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><head><meta property="og:title" content="Space Boy" />%s</head><body>%s
<li class="_episodeItem" data-episode-no="410"><a href="https://www.webtoons.com/en/sf/space-boy/ep-410/viewer?title_no=400&episode_no=410"><span class="date">Nov 6, 2026</span></a></li>
</body></html>`, head, body)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := connector.ResolveByURL(context.Background(), "https://www.webtoons.com/en/sf/space-boy/list?title_no=400")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 410 {
		t.Fatalf("expected latest chapter 410, got %v", resolved.LatestChapter)
	}
	return resolved
}

// TestWebtoonsMetadataOfARunningSeries pins the live page's shapes for an
// ongoing title: the schedule line means ongoing, the genre heading's label
// wins over the script's code ("Sci-fi", not "Sf"), and the synopsis falls back
// to the page's summary paragraph when there is no og:description.
func TestWebtoonsMetadataOfARunningSeries(t *testing.T) {
	resolved := resolveMetadataTestPage(t,
		`<meta property="com-linewebtoon:webtoon:author" content="Stephen McCranie" />`,
		`<h2 class="genre g_sf">Sci-fi</h2>
<p class="day_info"><span class="txt_ico_up">UP</span>EVERY THURSDAY</p>
<p class="summary">A girl who belongs in a different time. A boy possessed by an emptiness as deep as space.</p>
<script>var logParam = { titleNo: 400, representGenre: 'SF', contentLang: 'en' };</script>`)

	metadata := resolved.Metadata
	if metadata == nil {
		t.Fatal("expected metadata")
	}
	if metadata.Status != connectors.SeriesStatusOngoing {
		t.Fatalf("expected status ongoing, got %q", metadata.Status)
	}
	if strings.Join(metadata.Genres, "|") != "Sci-fi" {
		t.Fatalf("expected genres [Sci-fi], got %v", metadata.Genres)
	}
	if strings.Join(metadata.Authors, "|") != "Stephen McCranie" {
		t.Fatalf("expected authors [Stephen McCranie], got %v", metadata.Authors)
	}
	if metadata.Description != "A girl who belongs in a different time. A boy possessed by an emptiness as deep as space." {
		t.Fatalf("unexpected description %q", metadata.Description)
	}
}

// TestWebtoonsMetadataFallsBackToTheGenreCode pins the script's representGenre
// code as the genre source when the heading is gone.
func TestWebtoonsMetadataFallsBackToTheGenreCode(t *testing.T) {
	resolved := resolveMetadataTestPage(t, "", `<script>var logParam = { titleNo: 400, representGenre: 'SLICE_OF_LIFE' };</script>`)
	if resolved.Metadata == nil || strings.Join(resolved.Metadata.Genres, "|") != "Slice of life" {
		t.Fatalf("expected genres [Slice of life], got %+v", resolved.Metadata)
	}
	if got := genreLabelFromCode("SF"); got != "Sci-fi" {
		t.Fatalf("genreLabelFromCode(SF) = %q", got)
	}
	if got := genreLabelFromCode("ROMANCE"); got != "Romance" {
		t.Fatalf("genreLabelFromCode(ROMANCE) = %q", got)
	}
}

// TestWebtoonsMissingMetadataDoesNotFailTheResolve pins that a page with none
// of the metadata markup, or with only blank values, still resolves its
// chapter and simply reports no metadata.
func TestWebtoonsMissingMetadataDoesNotFailTheResolve(t *testing.T) {
	resolved := resolveMetadataTestPage(t,
		`<meta property="og:description" content="  " /><meta property="com-linewebtoon:webtoon:author" content=" / " />`,
		`<h2 class="genre"></h2><p class="day_info"></p>`)
	if resolved.Metadata != nil {
		t.Fatalf("expected no metadata, got %+v", resolved.Metadata)
	}
}

func TestWebtoonsSeriesStatus(t *testing.T) {
	cases := []struct {
		dayInfo string
		want    string
	}{
		{dayInfo: `<span class="txt_ico_up">UP</span>EVERY THURSDAY`, want: connectors.SeriesStatusOngoing},
		{dayInfo: `<span class="txt_ico_up">UP</span>MON, THU`, want: connectors.SeriesStatusOngoing},
		{dayInfo: `EVERY DAY`, want: connectors.SeriesStatusOngoing},
		{dayInfo: `<span class="txt_ico_completed"></span>COMPLETED`, want: connectors.SeriesStatusCompleted},
		{dayInfo: `COMPLETED`, want: connectors.SeriesStatusCompleted},
		// Both badges at once is a page this connector does not understand.
		{dayInfo: `<span class="txt_ico_completed"></span>UP EVERY THURSDAY`, want: ""},
		{dayInfo: `ON HIATUS`, want: connectors.SeriesStatusHiatus},
		{dayInfo: ``, want: ""},
		{dayInfo: `<span class="txt_ico_new">NEW</span>`, want: ""},
	}
	for _, tc := range cases {
		if got := seriesStatus(tc.dayInfo); got != tc.want {
			t.Errorf("seriesStatus(%q) = %q, want %q", tc.dayInfo, got, tc.want)
		}
	}
}

// The episode-list markup of a finished series the site now sells in its app,
// cut from the live page (Lore Olympus, 2026-11-08): the app-only notice, the
// free preview's rows (episodes 9 down to 1 of a run that ended at 280) and
// the share script's stamp of the last episode going up, 2024-06-23.
const (
	appOnlyNoticeMarkup = `<div class="detail_install_app">
	<strong>Read more episodes on the app!</strong><br/>Scan the QR code to download the <em>WEBTOON</em> app on the App Store or Google Play.
	<img src="/qr/app/episodeList/webtoon/1320" alt="QR Code" width="71" height="73" class="img_qrcode">
</div>`
	completedDayInfoMarkup = `<p class="day_info"><span class="txt_ico_completed"></span>COMPLETED</p>`
	loreOlympusRegistered  = "1719104476000"
)

// webtoonsEpisodeRow is one row of the live episode list's markup.
func webtoonsEpisodeRow(titleNo int, episodeNo int, date string) string {
	return fmt.Sprintf(`<li class="_episodeItem detail_list_item" id="episode_%[2]d" data-episode-no="%[2]d">
	<a href="https://www.webtoons.com/en/romance/lore-olympus/episode-%[2]d/viewer?title_no=%[1]d&episode_no=%[2]d" class="detail_list_link">
		<span class="thmb"><img src="https://webtoon-phinf.pstatic.net/thumb.png?type=f160_151" width="77" height="73" alt="Episode %[2]d"></span>
		<span class="subj"><span>Episode %[2]d</span></span>
		<span class="manage_blank"></span>
		<span class="date">%[3]s</span>
		<span class="like_area _likeitArea"><em class="ico_like _btnLike _likeMark">like</em>289,384</span>
		<span class="tx">#%[2]d</span>
	</a>
</li>`, titleNo, episodeNo, date)
}

// webtoonsEpisodeListPage assembles an episode-list page the way the live one
// lays it out: notice above the list, schedule line in the aside, and the
// share script carrying the last-registered stamp (left out when empty).
func webtoonsEpisodeListPage(title string, notice string, dayInfo string, registered string, rows ...string) string {
	script := ""
	if registered != "" {
		script = fmt.Sprintf(`<script type="text/javascript">
	window.__episodeListState__ = {
		shareComponentParam : {
			serviceZone: "GLOBAL",
			isChallenge : false,
			fbRefresh : "%s",
			languageCode : "en",
		},
	};
</script>`, registered)
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta property="og:title" content="%s" />
</head>
<body>
	<div class="detail_body banner">
		<div class="detail_list_area">
			%s
			<ul id="_listUl" class="detail_list">
			%s
			</ul>
		</div>
		<div class="aside detail" id="_asideDetail">
			%s
		</div>
	</div>
	%s
</body>
</html>`, title, notice, strings.Join(rows, "\n"), dayInfo, script)
}

// TestWebtoonsCompletedFreePreviewReportsNoChapter pins the finished series
// the site now sells in its app: its list shows only the free opening, and
// reporting the preview's newest episode (9) walked WEBTOON-primary trackers
// back from 280. The resolve answers with the title, link and metadata but no
// chapter; search keeps the hit without one; and the chapter-link lookup still
// finds the free episodes while calling the sold ones not carried, in one
// fetch.
func TestWebtoonsCompletedFreePreviewReportsNoChapter(t *testing.T) {
	var rows []string
	dates := []string{"Mar 4, 2018", "Mar 4, 2018", "Mar 4, 2018", "Mar 4, 2018", "Mar 11, 2018", "Mar 18, 2018", "Mar 25, 2018", "Apr 1, 2018", "Apr 8, 2018"}
	for episode := 9; episode >= 1; episode-- {
		rows = append(rows, webtoonsEpisodeRow(1320, episode, dates[episode-1]))
	}
	page := webtoonsEpisodeListPage("Lore Olympus", appOnlyNoticeMarkup, completedDayInfoMarkup, loreOlympusRegistered, rows...)

	var fetches atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/en/search/immediate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"query":"Lore Olympus","searchedList":[{"titleNo":1320,"title":"Lore Olympus","thumbnailMobile":"/thumb.jpg","authorNameList":["Rachel Smythe"],"representGenre":"ROMANCE","searchMode":"TITLE"}]},"success":true}`))
	})
	mux.HandleFunc("/episodeList", func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.URL.Query().Get("titleNo") != "1320" || r.URL.Query().Get("page") != "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(page))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	titleURL := "https://www.webtoons.com/en/romance/lore-olympus/list?title_no=1320"

	resolved, err := connector.ResolveByURL(context.Background(), titleURL)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter != nil || resolved.LastUpdatedAt != nil {
		t.Fatalf("expected no chapter and no date from a free preview, got %v at %v", *resolved.LatestChapter, resolved.LastUpdatedAt)
	}
	if resolved.Title != "Lore Olympus" || resolved.SourceItemID != "1320" {
		t.Fatalf("expected the title to resolve, got %q / %q", resolved.Title, resolved.SourceItemID)
	}
	if resolved.Metadata == nil || resolved.Metadata.Status != connectors.SeriesStatusCompleted {
		t.Fatalf("expected the completed status to come through, got %+v", resolved.Metadata)
	}

	results, err := connector.SearchByTitle(context.Background(), "Lore Olympus", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].LatestChapter != nil || results[0].LastUpdatedAt != nil {
		t.Fatalf("expected one search hit with no chapter, got %+v", results)
	}

	chapterURL, err := connector.ResolveChapterURL(context.Background(), titleURL, 9)
	if err != nil {
		t.Fatalf("resolve chapter url for a free episode failed: %v", err)
	}
	if want := "https://www.webtoons.com/en/romance/lore-olympus/episode-9/viewer?title_no=1320&episode_no=9"; chapterURL != want {
		t.Fatalf("expected %s, got %s", want, chapterURL)
	}

	fetches.Store(0)
	if _, err := connector.ResolveChapterURL(context.Background(), titleURL, 280); !errors.Is(err, connectors.ErrChapterNotFound) {
		t.Fatalf("expected a sold episode to be not carried, got %v", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("expected 1 page fetch, got %d", got)
	}
}

// TestWebtoonsFreePreviewDetection pins which lists count as a free preview.
// Only a finished series qualifies, the page's last-registered stamp decides
// when it can be compared with the newest row, and the app-only notice decides
// only when it cannot. The stamps and dates are the live pages' (2026-11-08).
func TestWebtoonsFreePreviewDetection(t *testing.T) {
	ongoingDayInfo := `<p class="day_info"><span class="txt_ico_up">UP</span>EVERY FRIDAY</p>`
	undatedRow := strings.Replace(webtoonsEpisodeRow(1320, 9, "Apr 8, 2018"), "Apr 8, 2018", "", 1)
	cases := []struct {
		name       string
		notice     string
		dayInfo    string
		registered string
		row        string
		want       float64 // 0 for no chapter
	}{
		{
			name: "finished, stamp years after the newest row", notice: appOnlyNoticeMarkup, dayInfo: completedDayInfoMarkup,
			registered: loreOlympusRegistered, row: webtoonsEpisodeRow(1320, 9, "Apr 8, 2018"), want: 0,
		},
		{
			name: "finished, stamp years after the newest row, no notice", dayInfo: completedDayInfoMarkup,
			registered: loreOlympusRegistered, row: webtoonsEpisodeRow(1320, 9, "Apr 8, 2018"), want: 0,
		},
		{
			// SubZero: finished, whole list, row and stamp the same day.
			name: "finished, stamp on the newest row's day", dayInfo: completedDayInfoMarkup,
			registered: "1792970100000", row: webtoonsEpisodeRow(1468, 278, "Oct 25, 2026"), want: 278,
		},
		{
			// A short finished series that shows the notice yet lists through
			// its last registered episode.
			name: "finished, notice, stamp the day after the newest row", notice: appOnlyNoticeMarkup, dayInfo: completedDayInfoMarkup,
			registered: "1547737224000", row: webtoonsEpisodeRow(1488, 10, "Jan 16, 2019"), want: 10,
		},
		{
			name: "finished, notice, no stamp", notice: appOnlyNoticeMarkup, dayInfo: completedDayInfoMarkup,
			row: webtoonsEpisodeRow(1320, 9, "Apr 8, 2018"), want: 0,
		},
		{
			name: "finished, notice, undated newest row", notice: appOnlyNoticeMarkup, dayInfo: completedDayInfoMarkup,
			registered: loreOlympusRegistered, row: undatedRow, want: 0,
		},
		{
			name: "finished, neither notice nor stamp", dayInfo: completedDayInfoMarkup,
			row: webtoonsEpisodeRow(1468, 278, "Oct 25, 2026"), want: 278,
		},
		{
			// I Love Yoo: running, with app-only episodes ahead of the web.
			name: "running, notice", notice: appOnlyNoticeMarkup, dayInfo: ongoingDayInfo,
			registered: "1792717272000", row: webtoonsEpisodeRow(986, 307, "Oct 22, 2026"), want: 307,
		},
		{
			name: "running, stamp years after the newest row", notice: appOnlyNoticeMarkup, dayInfo: ongoingDayInfo,
			registered: loreOlympusRegistered, row: webtoonsEpisodeRow(1320, 9, "Apr 8, 2018"), want: 9,
		},
		{
			name: "unknown status, notice, no stamp", notice: appOnlyNoticeMarkup,
			row: webtoonsEpisodeRow(1320, 9, "Apr 8, 2018"), want: 9,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := webtoonsEpisodeListPage("Some Title", tc.notice, tc.dayInfo, tc.registered, tc.row)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(page))
			}))
			defer server.Close()

			connector := NewConnectorWithOptions(server.URL, []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
			resolved, err := connector.ResolveByURL(context.Background(), "https://www.webtoons.com/en/x/list?title_no=1320")
			if err != nil {
				t.Fatalf("resolve failed: %v", err)
			}
			switch {
			case tc.want == 0 && resolved.LatestChapter != nil:
				t.Fatalf("expected no chapter, got %v", *resolved.LatestChapter)
			case tc.want != 0 && (resolved.LatestChapter == nil || *resolved.LatestChapter != tc.want):
				t.Fatalf("expected chapter %v, got %v", tc.want, resolved.LatestChapter)
			case tc.want != 0 && resolved.LastUpdatedAt == nil:
				t.Fatal("expected the newest row's date with its number")
			case tc.want == 0 && resolved.LastUpdatedAt != nil:
				t.Fatalf("expected no date without a chapter, got %v", resolved.LastUpdatedAt)
			}
		})
	}
}

func TestWebtoonsConnectorRejectsNonWebtoonsURL(t *testing.T) {
	connector := NewConnectorWithOptions("https://www.webtoons.com", []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := connector.ResolveByURL(context.Background(), "https://example.com/en/romance/maybe-meant-to-be/list?title_no=4208"); err == nil {
		t.Fatalf("expected non-webtoons url to fail")
	}
}
