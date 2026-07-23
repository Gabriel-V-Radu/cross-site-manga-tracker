package mangafire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newPagedChaptersServer serves a title with `total` English chapters numbered
// total..1 (newest first), paginated like the live API. Every /api endpoint
// requires a non-empty vrf query param, mirroring the "Missing token." gate, so
// the test also proves the connector signs its requests. It records how many
// chapter pages were requested.
func newPagedChaptersServer(t *testing.T, hid string, total int) (*httptest.Server, func() int) {
	t.Helper()

	var mu sync.Mutex
	pageRequests := 0

	mux := http.NewServeMux()
	requireToken := func(w http.ResponseWriter, r *http.Request) bool {
		if strings.TrimSpace(r.URL.Query().Get("vrf")) == "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Missing token."}`))
			return false
		}
		return true
	}

	mux.HandleFunc("/api/titles/"+hid+"/chapters", func(w http.ResponseWriter, r *http.Request) {
		if !requireToken(w, r) {
			return
		}
		mu.Lock()
		pageRequests++
		mu.Unlock()

		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 60
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}

		start := (page - 1) * limit // 0-based offset into the descending list
		var items []string
		for i := start; i < start+limit && i < total; i++ {
			number := total - i // descending: page 1 starts at the highest number
			id := 1_000_000 + number
			items = append(items, fmt.Sprintf(
				`{"id":%d,"number":%d,"language":"en","type":"official","createdAt":%d}`,
				id, number, 1_700_000_000+number))
		}
		hasNext := start+limit < total

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[%s],"meta":{"page":%d,"perPage":%d,"total":%d,"hasNext":%t}}`,
			strings.Join(items, ","), page, limit, total, hasNext)
	})

	server := httptest.NewServer(mux)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return pageRequests
	}
}

func TestMangaFireResolveChapterPaginates(t *testing.T) {
	const hid = "big"
	server, pageCount := newPagedChaptersServer(t, hid, 250)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})
	sourceURL := "https://mangafire.to/title/" + hid + "-some-title"

	// A chapter on the second page must still be found (the old single-request
	// fetch only saw the first page).
	chapterURL, err := connector.ResolveChapterURL(context.Background(), sourceURL, 10)
	if err != nil {
		t.Fatalf("resolve deep chapter: %v", err)
	}
	if want := "https://mangafire.to/title/" + hid + "-some-title/1000010"; chapterURL != want {
		t.Fatalf("unexpected chapter url:\n got  %s\n want %s", chapterURL, want)
	}
	if got := pageCount(); got < 2 {
		t.Fatalf("expected to page past the first page, only %d page(s) fetched", got)
	}
}

func TestMangaFireResolveRecentChapterEarlyExits(t *testing.T) {
	const hid = "big"
	server, pageCount := newPagedChaptersServer(t, hid, 250)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})
	sourceURL := "https://mangafire.to/title/" + hid + "-some-title"

	// Chapter 200 sits on the first page (numbers 250..51 at limit 200), so
	// resolution should not request any further pages.
	if _, err := connector.ResolveChapterURL(context.Background(), sourceURL, 200); err != nil {
		t.Fatalf("resolve recent chapter: %v", err)
	}
	if got := pageCount(); got != 1 {
		t.Fatalf("expected a single page request for a recent chapter, got %d", got)
	}
}

// TestMangaFireResolveChapterCrossesLanguageStraddle covers the case where a
// chapter number's non-English variant ends one page and its English variant
// begins the next. The resolver must keep paging until it is past the target
// number so pickChapterEntry can still prefer the English entry.
func TestMangaFireResolveChapterCrossesLanguageStraddle(t *testing.T) {
	const hid = "strad"

	// Ordered newest-first: 250..52 (en), then chapter 51 as [es, en], then 50..1 (en).
	type entry struct {
		number int
		lang   string
		id     int
	}
	var entries []entry
	for n := 250; n >= 52; n-- {
		entries = append(entries, entry{n, "en", 100000 + n})
	}
	entries = append(entries, entry{51, "es", 900051}) // tail of page 1 at limit 200
	entries = append(entries, entry{51, "en", 100051}) // head of page 2
	for n := 50; n >= 1; n-- {
		entries = append(entries, entry{n, "en", 100000 + n})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/"+hid+"/chapters", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.URL.Query().Get("vrf")) == "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Missing token."}`))
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 60
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}
		start := (page - 1) * limit
		var items []string
		for i := start; i < start+limit && i < len(entries); i++ {
			e := entries[i]
			items = append(items, fmt.Sprintf(
				`{"id":%d,"number":%d,"language":%q,"type":"official","createdAt":%d}`,
				e.id, e.number, e.lang, 1_700_000_000+e.number))
		}
		hasNext := start+limit < len(entries)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[%s],"meta":{"hasNext":%t}}`, strings.Join(items, ","), hasNext)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})
	chapterURL, err := connector.ResolveChapterURL(context.Background(), "https://mangafire.to/title/"+hid+"-x", 51)
	if err != nil {
		t.Fatalf("resolve straddled chapter: %v", err)
	}
	if want := "https://mangafire.to/title/" + hid + "-x/100051"; chapterURL != want {
		t.Fatalf("expected English variant across page boundary:\n got  %s\n want %s", chapterURL, want)
	}
}

// TestMangaFireTokenRejectionIsDistinct verifies a 403 token error (stale/absent
// signer) is surfaced as a signing problem, not a Cloudflare rate-limit.
func TestMangaFireTokenRejectionIsDistinct(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Invalid token."}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	err := connector.HealthCheck(context.Background())
	if err == nil {
		t.Fatalf("expected error on token rejection")
	}
	if !strings.Contains(err.Error(), "rejected request token") {
		t.Fatalf("expected a distinct token-rejection error, got: %v", err)
	}

	// The subsequent cooldown must also describe the token problem, not a rate-limit.
	_, err = connector.SearchByTitle(context.Background(), "one piece", 5)
	if err == nil {
		t.Fatalf("expected fail-fast during cooldown")
	}
	if !strings.Contains(err.Error(), "signer token rejected") {
		t.Fatalf("expected cooldown to cite the token problem, got: %v", err)
	}
}

func TestMangaFireRequestsCarryToken(t *testing.T) {
	const hid = "big"
	server, _ := newPagedChaptersServer(t, hid, 30)
	defer server.Close()

	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})
	sourceURL := "https://mangafire.to/title/" + hid + "-some-title"

	// The server rejects any request lacking a vrf token; a successful resolve
	// proves the connector signed the request.
	if _, err := connector.ResolveChapterURL(context.Background(), sourceURL, 5); err != nil {
		t.Fatalf("resolve with token failed (signing not applied?): %v", err)
	}
}
