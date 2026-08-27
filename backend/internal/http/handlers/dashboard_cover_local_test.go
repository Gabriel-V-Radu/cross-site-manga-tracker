package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// pngBytes is a minimal valid PNG header, enough for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func newLocalCoverHandler(t *testing.T, registry *connectors.Registry) *DashboardHandler {
	t.Helper()
	h := newFallbackHandler(t, registry)
	// The real download path, not the test checker: these tests exercise the
	// local store itself against a local HTTP server.
	h.coverURLChecker = nil
	h.coverDir = t.TempDir()
	return h
}

// TestFetchCoverURLStoresLocalCopy pins the local cover store end to end: the
// resolved image is downloaded once, served from /covers thereafter, and the
// cache entry survives without re-touching the image host.
func TestFetchCoverURLStoresLocalCopy(t *testing.T) {
	var hits atomic.Int64
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer imageServer.Close()

	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource", cover: imageServer.URL + "/cover.png"}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	h := newLocalCoverHandler(t, registry)

	served, err := h.fetchCoverURL(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil)
	if err != nil {
		t.Fatalf("fetch cover: %v", err)
	}
	if !strings.HasPrefix(served, coverLocalURLPrefix) || !strings.HasSuffix(served, ".png") {
		t.Fatalf("expected a local /covers URL, got %q", served)
	}

	name := strings.TrimPrefix(served, coverLocalURLPrefix)
	if _, err := os.Stat(filepath.Join(h.coverDir, name)); err != nil {
		t.Fatalf("expected the image stored on disk: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected exactly one download, got %d", hits.Load())
	}

	// The cached entry serves the same local URL without another download,
	// and outlives any nominal remote TTL.
	cacheKey := buildCoverCacheKey("primarysource", "https://primary.example/title/a", nil)
	cached, sourceKey, found, ok := h.getCachedCoverWithSource(cacheKey)
	if !ok || !found || cached != served {
		t.Fatalf("expected the local URL cached, got %q (found=%v ok=%v)", cached, found, ok)
	}
	if sourceKey != "primarysource" {
		t.Fatalf("expected the serving source recorded, got %q", sourceKey)
	}
	if hits.Load() != 1 {
		t.Fatalf("a cache read must not re-download, got %d hits", hits.Load())
	}
}

// TestFetchCoverURLRejectsNonImageBodies keeps challenge pages and CDN error
// bodies out of the store: a download that is not recognizably an image falls
// through to the next source instead of being served as art forever.
func TestFetchCoverURLRejectsNonImageBodies(t *testing.T) {
	challengeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Checking your browser</body></html>"))
	}))
	defer challengeServer.Close()

	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource", cover: challengeServer.URL + "/cover.jpg"}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	h := newLocalCoverHandler(t, registry)

	if _, err := h.fetchCoverURL(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected a non-image body to be rejected")
	}
	files, err := os.ReadDir(h.coverDir)
	if err != nil {
		t.Fatalf("read cover dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected nothing stored, got %v", files)
	}
}

// TestCachedLocalCoverHealsWhenTheFileVanishes pins the self-repair: an entry
// whose file is gone (a wiped volume, a manual cleanup) reads as a miss so
// the cover is re-fetched, rather than rendering a broken image forever.
func TestCachedLocalCoverHealsWhenTheFileVanishes(t *testing.T) {
	h := newLocalCoverHandler(t, connectors.NewRegistry())

	cacheKey := buildCoverCacheKey("primarysource", "https://primary.example/title/a", nil)
	h.setCachedCoverLocal(cacheKey, "https://cdn.example/cover.png", "gone.png", "primarysource")

	if _, _, ok := h.getCachedCover(cacheKey); ok {
		t.Fatalf("expected a missing file to read as a cache miss")
	}
	h.cacheMu.RLock()
	_, stillThere := h.coverCache[cacheKey]
	h.cacheMu.RUnlock()
	if stillThere {
		t.Fatalf("expected the dangling entry to be dropped")
	}
}

// TestCachedLocalCoverIgnoresExpiry pins permanence: a local entry far past
// its nominal expiry still serves, because the file on disk is the cache.
func TestCachedLocalCoverIgnoresExpiry(t *testing.T) {
	h := newLocalCoverHandler(t, connectors.NewRegistry())

	name := "kept.png"
	if err := os.WriteFile(filepath.Join(h.coverDir, name), pngBytes, 0o644); err != nil {
		t.Fatalf("write cover file: %v", err)
	}

	cacheKey := buildCoverCacheKey("primarysource", "https://primary.example/title/a", nil)
	h.cacheMu.Lock()
	h.coverCache[cacheKey] = coverCacheEntry{
		CoverURL:  "https://cdn.example/cover.png",
		Found:     true,
		SourceKey: "primarysource",
		ExpiresAt: time.Now().UTC().Add(-24 * time.Hour),
		LocalPath: name,
	}
	h.cacheMu.Unlock()

	cached, found, ok := h.getCachedCover(cacheKey)
	if !ok || !found || cached != coverLocalURLPrefix+name {
		t.Fatalf("expected the expired local entry to keep serving, got %q (found=%v ok=%v)", cached, found, ok)
	}
}
