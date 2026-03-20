package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type sourceURLResolverStub struct{}

func (sourceURLResolverStub) Key() string {
	return "asuracomic"
}

func (sourceURLResolverStub) Name() string {
	return "AsuraComic"
}

func (sourceURLResolverStub) Kind() string {
	return connectors.KindNative
}

func (sourceURLResolverStub) HealthCheck(context.Context) error {
	return nil
}

func (sourceURLResolverStub) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return &connectors.MangaResult{URL: "https://asurascans.com/comics/i-am-the-fated-villain-7f873ca6"}, nil
}

func (sourceURLResolverStub) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

func TestHasResolvedSourceMetadataRequiresReleaseDate(t *testing.T) {
	chapter := 42.0
	itemID := "mangadex-item"
	now := time.Now().UTC()

	trackerWithoutRelease := &models.Tracker{
		SourceItemID:       &itemID,
		LatestKnownChapter: &chapter,
	}
	if hasResolvedSourceMetadata(trackerWithoutRelease) {
		t.Fatalf("expected metadata without latest release date to be incomplete")
	}

	trackerWithRelease := &models.Tracker{
		SourceItemID:       &itemID,
		LatestKnownChapter: &chapter,
		LatestReleaseAt:    &now,
	}
	if !hasResolvedSourceMetadata(trackerWithRelease) {
		t.Fatalf("expected metadata with source item id, latest chapter, and release date to be complete")
	}
}

func TestSourceHomeURLForKeySupportsMgeko(t *testing.T) {
	homeURL := sourceHomeURLForKey("mgeko")
	if homeURL != "https://www.mgeko.cc" {
		t.Fatalf("expected mgeko home url, got %q", homeURL)
	}
}

func TestInferSourceKeyFromURLSupportsMgeko(t *testing.T) {
	inferred := inferSourceKeyFromURL("https://www.mgeko.cc/manga/sample-series/")
	if inferred != "mgeko" {
		t.Fatalf("expected inferred source key mgeko, got %q", inferred)
	}
}

func TestGetCachedOrQueueSourceURLResolvesCanonicalAsuraURL(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(sourceURLResolverStub{}); err != nil {
		t.Fatalf("register source url resolver: %v", err)
	}

	h := &DashboardHandler{
		registry:          registry,
		sourceURLCache:    make(map[string]sourceURLCacheEntry),
		sourceURLInFlight: make(map[string]bool),
		sourceURLFetchSem: make(chan struct{}, 1),
	}

	staleURL := "https://asurascans.com/series/i-am-the-fated-villain-7b38ced7"
	resolvedURL, waiting := h.getCachedOrQueueSourceURL("asuracomic", staleURL, "")
	if resolvedURL != staleURL {
		t.Fatalf("expected initial source url %q, got %q", staleURL, resolvedURL)
	}
	if !waiting {
		t.Fatalf("expected source url resolution to be queued")
	}

	cacheKey := buildSourceURLCacheKey("asuracomic", staleURL)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if canonicalURL, found, ok := h.getCachedSourceURL(cacheKey); ok && found {
			expected := "https://asurascans.com/comics/i-am-the-fated-villain-7f873ca6"
			if canonicalURL != expected {
				t.Fatalf("expected cached source url %q, got %q", expected, canonicalURL)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected source url cache entry for asuracomic")
}

func TestBuildTrackerCardsDoesNotUseLastCheckedAtAsReleaseDate(t *testing.T) {
	lastCheckedAt := time.Now().UTC()
	h := &DashboardHandler{}
	items := []models.Tracker{{
		ID:            1,
		Title:         "Sample",
		Status:        "reading",
		SourceID:      1,
		SourceURL:     "https://example.com/series/sample",
		LastCheckedAt: &lastCheckedAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, pending := h.buildTrackerCards(items, sourceByID, map[int64]string{}, "")
	if pending {
		t.Fatalf("expected no asynchronous lookups for source without connector key")
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "—" {
		t.Fatalf("expected unknown release date marker, got %q", cards[0].LatestReleaseAgo)
	}
}
