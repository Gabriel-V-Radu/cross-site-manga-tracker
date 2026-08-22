package comick

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestConnector(t *testing.T, handler http.Handler) *Connector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewConnectorWithOptions(
		"https://comick.dev",
		server.URL,
		[]string{"comick.dev", "comick.io", "comick.fun"},
		&http.Client{Timeout: 5 * time.Second},
	)
}

func kagurabachiHandler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"hid":"10ZRNmsG","slug":"kagura-bachi","title":"Kagurabachi",
			 "md_titles":[{"title":"Kagurabachi"},{"title":"Kagura Bowl"},{"title":"カグラバチ"}],
			 "md_covers":[{"b2key":"KrNwor.jpg"}]},
			{"hid":"","slug":"broken","title":"Missing HID"}
		]`))
	})
	mux.HandleFunc("/comic/kagura-bachi", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comic":{"hid":"10ZRNmsG","slug":"kagura-bachi","title":"Kagurabachi",
			"md_titles":[{"title":"Kagura Bowl"},{"title":"カグラバチ"}],
			"md_covers":[{"b2key":"KrNwor.jpg"}]}}`))
	})
	mux.HandleFunc("/comic/10ZRNmsG/chapters", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("chap") == "100" {
			_, _ = w.Write([]byte(`{"chapters":[{"hid":"abcHID99","chap":"100","lang":"en","publish_at":"2025-11-10T01:06:48.000Z"}]}`))
			return
		}
		// A numberless special sits above the real latest chapter.
		_, _ = w.Write([]byte(`{"chapters":[
			{"hid":"spc","chap":null,"lang":"en","publish_at":"2026-08-10T00:00:00.000Z"},
			{"hid":"3wxB57iv","chap":"128","lang":"en","publish_at":"2026-08-09T15:20:12.000Z"},
			{"hid":"prev","chap":"127","lang":"en","publish_at":"2026-08-02T15:00:00.000Z"}
		]}`))
	})
	return mux
}

func TestResolveByURLReadsEnglishChapterAndTitles(t *testing.T) {
	connector := newTestConnector(t, kagurabachiHandler(t))

	result, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.Title != "Kagurabachi" {
		t.Fatalf("unexpected title %q", result.Title)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 128 {
		t.Fatalf("expected latest chapter 128, got %#v", result.LatestChapter)
	}
	want := time.Date(2026, 8, 9, 15, 20, 12, 0, time.UTC)
	if result.LastUpdatedAt == nil || !result.LastUpdatedAt.Equal(want) {
		t.Fatalf("expected publish time %v, got %v", want, result.LastUpdatedAt)
	}
	if result.SourceItemID != "10ZRNmsG" {
		t.Fatalf("unexpected source item id %q", result.SourceItemID)
	}
	if result.URL != "https://comick.dev/comic/kagura-bachi" {
		t.Fatalf("unexpected canonical url %q", result.URL)
	}
	if result.CoverImageURL != "https://meo.comick.pictures/KrNwor.jpg" {
		t.Fatalf("unexpected cover %q", result.CoverImageURL)
	}
	// The main title is excluded; the non-latin one is kept here and filtered
	// downstream where related titles are persisted.
	if len(result.RelatedTitles) != 2 || result.RelatedTitles[0] != "Kagura Bowl" {
		t.Fatalf("unexpected related titles %v", result.RelatedTitles)
	}
}

func TestResolveByURLAcceptsChapterURLs(t *testing.T) {
	connector := newTestConnector(t, kagurabachiHandler(t))

	result, err := connector.ResolveByURL(context.Background(), "https://comick.io/comic/kagura-bachi/3wxB57iv-chapter-128-en")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 128 {
		t.Fatalf("expected latest chapter 128, got %#v", result.LatestChapter)
	}
}

func TestResolveByURLRejectsForeignHosts(t *testing.T) {
	connector := newTestConnector(t, kagurabachiHandler(t))
	if _, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/x"); err == nil {
		t.Fatalf("expected foreign host to be rejected")
	}
}

func TestSearchByTitleMapsResultsWithoutChapterNumbers(t *testing.T) {
	connector := newTestConnector(t, kagurabachiHandler(t))

	results, err := connector.SearchByTitle(context.Background(), "kagurabachi", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the hid-less record to be dropped, got %d results", len(results))
	}
	if results[0].URL != "https://comick.dev/comic/kagura-bachi" {
		t.Fatalf("unexpected url %q", results[0].URL)
	}
	if results[0].LatestChapter != nil {
		t.Fatalf("search results must not carry cross-language chapter counts, got %v", *results[0].LatestChapter)
	}
}

func TestResolveChapterURLBuildsReaderLink(t *testing.T) {
	connector := newTestConnector(t, kagurabachiHandler(t))

	url, err := connector.ResolveChapterURL(context.Background(), "https://comick.dev/comic/kagura-bachi", 100)
	if err != nil {
		t.Fatalf("resolve chapter failed: %v", err)
	}
	if url != "https://comick.dev/comic/kagura-bachi/abcHID99-chapter-100-en" {
		t.Fatalf("unexpected chapter url %q", url)
	}
}

// TestResolveByURLCachesComicRecord pins the one-request steady state: the
// comic payload (hid, title, cover) is stable per slug, so repeat resolves
// must only hit the chapters endpoint. The API's host gap is a wide 3.5s and
// dashboard cover passes fire dozens of resolves at once, so every avoided
// request is real queue time.
func TestResolveByURLCachesComicRecord(t *testing.T) {
	comicHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/comic/kagura-bachi", func(w http.ResponseWriter, _ *http.Request) {
		comicHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comic":{"hid":"10ZRNmsG","slug":"kagura-bachi","title":"Kagurabachi"}}`))
	})
	mux.HandleFunc("/comic/10ZRNmsG/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chapters":[{"hid":"3wxB57iv","chap":"128","lang":"en","publish_at":"2026-08-09T15:20:12.000Z"}]}`))
	})
	connector := newTestConnector(t, mux)

	for i := 0; i < 3; i++ {
		if _, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi"); err != nil {
			t.Fatalf("resolve %d failed: %v", i+1, err)
		}
	}
	if comicHits != 1 {
		t.Fatalf("expected 1 comic fetch across repeat resolves, got %d", comicHits)
	}
}

// TestResolveByURLHealsStaleCachedHid covers a comic that moved: the cached
// hid starts answering 404 on chapters, and the resolve must refetch the
// record instead of failing forever until the TTL expires.
func TestResolveByURLHealsStaleCachedHid(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/comic/kagura-bachi", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comic":{"hid":"freshHID","slug":"kagura-bachi","title":"Kagurabachi"}}`))
	})
	mux.HandleFunc("/comic/staleHID/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/comic/freshHID/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chapters":[{"hid":"x","chap":"128","lang":"en"}]}`))
	})
	connector := newTestConnector(t, mux)
	connector.storeComic("kagura-bachi", comicRecord{hid: "staleHID", title: "Kagurabachi", fetchedAt: time.Now()})

	result, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 128 {
		t.Fatalf("expected chapter 128 after healing, got %#v", result.LatestChapter)
	}
}

func TestResolveByURLWithoutEnglishChaptersLeavesChapterUnset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/comic/silent", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comic":{"hid":"noEN","slug":"silent","title":"Silent"}}`))
	})
	mux.HandleFunc("/comic/noEN/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chapters":[]}`))
	})
	connector := newTestConnector(t, mux)

	result, err := connector.ResolveByURL(context.Background(), "https://comick.dev/comic/silent")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LatestChapter != nil || result.LastUpdatedAt != nil {
		t.Fatalf("expected no chapter without english uploads, got %#v %#v", result.LatestChapter, result.LastUpdatedAt)
	}
}
