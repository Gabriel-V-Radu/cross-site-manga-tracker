package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

type mangaFireChapterResolverStub struct{}

func (mangaFireChapterResolverStub) Key() string {
	return "mangafire"
}

func (mangaFireChapterResolverStub) Name() string {
	return "MangaFire"
}

func (mangaFireChapterResolverStub) Kind() string {
	return connectors.KindNative
}

func (mangaFireChapterResolverStub) HealthCheck(context.Context) error {
	return nil
}

func (mangaFireChapterResolverStub) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, nil
}

func (mangaFireChapterResolverStub) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

func (mangaFireChapterResolverStub) ResolveChapterURL(_ context.Context, rawURL string, chapter float64) (string, error) {
	return rawURL + "#chapter=" + formatChapterLabel(chapter), nil
}

func TestExtractMangaFireMangaURL(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantURL string
		wantOK  bool
	}{
		{
			name:    "accepts valid mangafire manga URL",
			query:   "https://mangafire.to/manga/one-piecee.dkw",
			wantURL: "https://mangafire.to/manga/one-piecee.dkw",
			wantOK:  true,
		},
		{
			name:    "strips query and fragment",
			query:   "https://www.mangafire.to/manga/one-piecee.dkw?x=1#top",
			wantURL: "https://www.mangafire.to/manga/one-piecee.dkw",
			wantOK:  true,
		},
		{
			name:   "rejects title text",
			query:  "One Piece",
			wantOK: false,
		},
		{
			name:   "rejects embedded URL in free text",
			query:  "check this https://mangafire.to/manga/one-piecee.dkw",
			wantOK: false,
		},
		{
			name:   "rejects non manga path",
			query:  "https://mangafire.to/read/one-piecee.dkw/en/chapter-1",
			wantOK: false,
		},
		{
			name:   "rejects other domains",
			query:  "https://example.com/manga/one-piecee.dkw",
			wantOK: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			gotURL, gotOK := extractMangaFireMangaURL(testCase.query)
			if gotOK != testCase.wantOK {
				t.Fatalf("expected ok=%v, got %v", testCase.wantOK, gotOK)
			}
			if gotURL != testCase.wantURL {
				t.Fatalf("expected url %q, got %q", testCase.wantURL, gotURL)
			}
		})
	}
}

func TestGetCachedOrQueueChapterURL_MangaFireQueuesResolver(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mangaFireChapterResolverStub{}); err != nil {
		t.Fatalf("register mangafire connector: %v", err)
	}

	h := &DashboardHandler{
		registry:           registry,
		chapterURLCache:    make(map[string]chapterURLCacheEntry),
		chapterURLInFlight: make(map[string]bool),
		chapterURLFetchSem: make(chan struct{}, 1),
	}

	sourceURL := "https://mangafire.to/manga/one-piecee.dkw"
	chapter := 1173.0

	resolvedURL, waiting := h.getCachedOrQueueChapterURL("mangafire", sourceURL, chapter, "")
	if resolvedURL != sourceURL {
		t.Fatalf("expected initial URL %q, got %q", sourceURL, resolvedURL)
	}
	if !waiting {
		t.Fatalf("expected mangafire chapter url resolution to be queued")
	}

	cacheKey := buildChapterURLCacheKey("mangafire", sourceURL, chapter)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if chapterURL, found, ok := h.getCachedChapterURL(cacheKey); ok && found {
			expected := sourceURL + "#chapter=Ch. 1173"
			if chapterURL != expected {
				t.Fatalf("expected cached chapter URL %q, got %q", expected, chapterURL)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected chapter URL cache entry for mangafire")
}
