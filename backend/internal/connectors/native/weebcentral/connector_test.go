package weebcentral

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSeriesID = "01J76XYDT7H7ANER8KJG5R9SJV"

// seriesPageFixture mirrors the real markup: og meta in the head, chapter
// anchors with a varying label prefix and a per-chapter <time datetime>.
const seriesPageFixture = `<!DOCTYPE html><html><head>
<meta property="og:title" content="Nano Machine | Weeb Central">
<meta property="og:image" content="https://temp.compsci88.com/cover/fallback/01J76XYDT7H7ANER8KJG5R9SJV.jpg">
</head><body>
<strong>Associated Name(s)</strong>
<ul class="list-disc list-inside">
    <li>Nano Mashin</li>
    <li>&#48208;&#47560;&#49828;&#53440;</li>
</ul>
<a href="/chapters/01M0E7K6FVFS7MAQT31A5RMS7Y" class="hover:bg-base-300 flex-1 flex items-center p-2">
    <span class="grow flex items-center gap-2">
        <span class="">Episode 326</span>
    </span>
    <time class="text-datetime opacity-50" datetime="2026-08-20T00:02:06.459Z">2026-08-20T00:02:06.459356Z</time>
</a>
<a href="/chapters/01M0E7K6FVFS7MAQT31A5RMS7Z" class="hover:bg-base-300 flex-1 flex items-center p-2">
    <span class="grow flex items-center gap-2">
        <span class="">Episode 325</span>
    </span>
    <time class="text-datetime opacity-50" datetime="2026-08-12T23:56:00.795Z">2026-08-12T23:56:00.795Z</time>
</a>
<a href="/chapters/01M0E7K6FVFS7MAQT31A5RMS7W" class="hover:bg-base-300 flex-1 flex items-center p-2">
    <span class="grow flex items-center gap-2">
        <span class="">Epilogue</span>
    </span>
    <time class="text-datetime opacity-50" datetime="2026-08-01T00:00:00.000Z">2026-08-01T00:00:00.000Z</time>
</a>
</body></html>`

const searchFragmentFixture = `<section id="quick-search-result">
<a href="https://weebcentral.com/series/01J76XYDT7H7ANER8KJG5R9SJV/Nano-Machine" class="btn join-item h-20">
    <picture><img src="https://temp.compsci88.com/cover/fallback/01J76XYDT7H7ANER8KJG5R9SJV.jpg" alt="Nano Machine cover"></picture>
    <div class="flex-1 overflow-hidden text-left">Nano Machine</div>
</a>
<a href="https://weebcentral.com/series/01J76XYDVYTFGC5FCED4R5AKN3/Rebellio-Machina" class="btn join-item h-20">
    <picture><img src="https://temp.compsci88.com/cover/fallback/01J76XYDVYTFGC5FCED4R5AKN3.jpg" alt="Rebellio Machina cover"></picture>
    <div class="flex-1 overflow-hidden text-left">Rebellio Machina</div>
</a>
</section>`

func newTestServer(t *testing.T) (*httptest.Server, *Connector) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/series/"+testSeriesID, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(seriesPageFixture))
	})
	mux.HandleFunc("/series/"+testSeriesID+"/full-chapter-list", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(seriesPageFixture))
	})
	mux.HandleFunc("/search/simple", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil || strings.TrimSpace(r.PostFormValue("text")) == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(searchFragmentFixture))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	serverURL := server.URL
	host := strings.TrimPrefix(serverURL, "http://")
	hostname := host
	if colon := strings.IndexByte(host, ':'); colon >= 0 {
		hostname = host[:colon]
	}
	connector := NewConnectorWithOptions(serverURL, []string{hostname}, &http.Client{Timeout: 5 * time.Second})
	return server, connector
}

func TestResolveByURLParsesSeriesPage(t *testing.T) {
	server, connector := newTestServer(t)

	result, err := connector.ResolveByURL(context.Background(), server.URL+"/series/"+testSeriesID+"/Nano-Machine")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if result.Title != "Nano Machine" {
		t.Fatalf("title = %q", result.Title)
	}
	if result.SourceItemID != testSeriesID {
		t.Fatalf("source item id = %q", result.SourceItemID)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 326 {
		t.Fatalf("latest chapter = %v, want 326", result.LatestChapter)
	}
	if result.LastUpdatedAt == nil {
		t.Fatal("expected a release timestamp")
	}
	want := time.Date(2026, 8, 20, 0, 2, 6, 459000000, time.UTC)
	if !result.LastUpdatedAt.Equal(want) {
		t.Fatalf("release at = %v, want %v", result.LastUpdatedAt, want)
	}
	if !strings.Contains(result.CoverImageURL, "cover/fallback") {
		t.Fatalf("cover = %q", result.CoverImageURL)
	}
	// The non-English associated name is filtered; the Latin one survives.
	if len(result.RelatedTitles) != 1 || result.RelatedTitles[0] != "Nano Mashin" {
		t.Fatalf("related titles = %v", result.RelatedTitles)
	}
}

func TestResolveByURLRejectsForeignHost(t *testing.T) {
	_, connector := newTestServer(t)

	if _, err := connector.ResolveByURL(context.Background(), "https://example.com/series/"+testSeriesID); err == nil {
		t.Fatal("expected foreign host to be rejected")
	}
}

func TestSearchByTitleFiltersToQuery(t *testing.T) {
	_, connector := newTestServer(t)

	results, err := connector.SearchByTitle(context.Background(), "Nano Machine", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (Rebellio Machina must be filtered out)", len(results))
	}
	if results[0].Title != "Nano Machine" || results[0].SourceItemID != testSeriesID {
		t.Fatalf("unexpected result: %+v", results[0])
	}
	if !strings.HasSuffix(results[0].URL, "/series/"+testSeriesID) {
		t.Fatalf("url = %q", results[0].URL)
	}
}

func TestResolveChapterURLFindsChapterID(t *testing.T) {
	server, connector := newTestServer(t)

	chapterURL, err := connector.ResolveChapterURL(context.Background(), server.URL+"/series/"+testSeriesID, 325)
	if err != nil {
		t.Fatalf("resolve chapter: %v", err)
	}
	if !strings.HasSuffix(chapterURL, "/chapters/01M0E7K6FVFS7MAQT31A5RMS7Z") {
		t.Fatalf("chapter url = %q", chapterURL)
	}

	if _, err := connector.ResolveChapterURL(context.Background(), server.URL+"/series/"+testSeriesID, 999); err == nil {
		t.Fatal("expected missing chapter to error")
	}
}

// TestLabelPrefixVariants pins that only the trailing number of a chapter
// label is trusted: the prefix word varies per series on WeebCentral.
func TestLabelPrefixVariants(t *testing.T) {
	body := `
<a href="/chapters/01M0E7K6FVFS7MAQT31A5RMS11"><span class="">Chapter 17.2</span><time datetime="2026-08-21T00:00:00Z">x</time></a>
<a href="/chapters/01M0E7K6FVFS7MAQT31A5RMS12"><span class="">Suggestion 192</span><time datetime="2024-09-07T00:00:00Z">x</time></a>
<a href="/chapters/01M0E7K6FVFS7MAQT31A5RMS13"><span class="">flower 13521</span><time datetime="2024-09-07T00:00:00Z">x</time></a>`

	latest, _ := extractLatestChapter(body)
	if latest == nil || *latest != 192 {
		t.Fatalf("latest = %v, want 192 (13521 is beyond the plausibility guard)", latest)
	}
}
