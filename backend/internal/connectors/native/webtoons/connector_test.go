package webtoons

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

	searchResults, err := connector.SearchByTitle(context.Background(), "Maybe Meant to Be", 8)
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

func TestWebtoonsConnectorRejectsNonWebtoonsURL(t *testing.T) {
	connector := NewConnectorWithOptions("https://www.webtoons.com", []string{"webtoons.com"}, &http.Client{Timeout: 5 * time.Second})
	if _, err := connector.ResolveByURL(context.Background(), "https://example.com/en/romance/maybe-meant-to-be/list?title_no=4208"); err == nil {
		t.Fatalf("expected non-webtoons url to fail")
	}
}
