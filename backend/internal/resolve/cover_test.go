package resolve

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
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// pngBytes is a minimal valid PNG header, enough for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// newLocalCoverResolver exercises the download path itself, against a local
// HTTP server: the guarded client would refuse 127.0.0.1, which is the point of
// it, so these tests name the unguarded one explicitly.
func newLocalCoverResolver(t *testing.T, registry *connectors.Registry) *CoverResolver {
	t.Helper()
	resolver := NewCoverResolver(CoverConfig{
		Registry: registry,
		Dir:      t.TempDir(),
		Client:   coverProbeClient,
	})
	t.Cleanup(resolver.Close)
	return resolver
}

// TestGuardedClientIsTheDefault pins the seam the SSRF guard hangs on: a
// resolver that names no client fetches through the guarded one. Cover URLs are
// scraped from third-party pages, so a config that forgets the client must not
// silently open the Pi's LAN to whoever controls a source.
func TestGuardedClientIsTheDefault(t *testing.T) {
	resolver := NewCoverResolver(CoverConfig{Registry: connectors.NewRegistry()})
	t.Cleanup(resolver.Close)

	if resolver.client != guardedCoverClient {
		t.Fatalf("expected the guarded cover client by default")
	}
}

// TestFetchCoverURLFallsBackToAlternate covers the case that left a whole library
// without cover art: the primary source is blocked, but the tracker has a linked
// mirror that answers.
func TestFetchCoverURLFallsBackToAlternate(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "mirrorsource", cover: "https://cdn.example/cover.webp"}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	resolver := newCoverTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	coverURL, err := resolver.fetch(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, alternates)
	if err != nil {
		t.Fatalf("expected the alternate to supply a cover: %v", err)
	}
	if coverURL != "https://cdn.example/cover.webp" {
		t.Fatalf("unexpected cover url %q", coverURL)
	}

	// The result must be cached under the primary key, so the next render of the
	// same tracker is served without re-querying either site.
	cacheKey := coverCacheKey("blockedsource", "https://blocked.example/title/a", nil)
	cached, found, ok := resolver.cached(cacheKey)
	if !ok || !found {
		t.Fatalf("expected the fallback cover to be cached under the primary key")
	}
	if cached != coverURL {
		t.Fatalf("cached cover %q does not match resolved %q", cached, coverURL)
	}
}

// TestFetchCoverURLRecordsServingSource checks the cache remembers which site
// answered, since the card badge is driven from it.
func TestFetchCoverURLRecordsServingSource(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "mirrorsource", cover: "https://cdn.example/cover.webp"}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	resolver := newCoverTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceID: 9, SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	if _, err := resolver.fetch(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, alternates); err != nil {
		t.Fatalf("expected the alternate to supply a cover: %v", err)
	}

	cacheKey := coverCacheKey("blockedsource", "https://blocked.example/title/a", nil)
	_, servingKey, found, ok := resolver.cachedWithSource(cacheKey)
	if !ok || !found {
		t.Fatalf("expected a cached cover")
	}
	if servingKey != "mirrorsource" {
		t.Fatalf("expected the mirror to be recorded as the serving source, got %q", servingKey)
	}
}

// TestFetchCoverURLRecordsPrimaryWhenItServes is the counterpart: a healthy
// primary must still be reported as the serving source.
func TestFetchCoverURLRecordsPrimaryWhenItServes(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource", cover: "https://cdn.example/primary.webp"}); err != nil {
		t.Fatalf("register primary connector: %v", err)
	}

	resolver := newCoverTestResolver(t, registry)

	if _, err := resolver.fetch(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	cacheKey := coverCacheKey("primarysource", "https://primary.example/title/a", nil)
	_, servingKey, _, _ := resolver.cachedWithSource(cacheKey)
	if servingKey != "primarysource" {
		t.Fatalf("expected the primary source to be recorded, got %q", servingKey)
	}
}

// TestFetchCoverURLSkipsUnloadableCovers pins the ComicK CDN outage shape: the
// API keeps answering with cover URLs whose image host is dead, and the card
// must fall through to a linked source whose image actually loads instead of
// caching the broken URL for 12 hours.
func TestFetchCoverURLSkipsUnloadableCovers(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource", cover: "https://dead.cdn.example/cover.webp"}); err != nil {
		t.Fatalf("register primary connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "mirrorsource", cover: "https://live.cdn.example/cover.webp"}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	resolver := NewCoverResolver(CoverConfig{
		Registry: registry,
		URLChecker: func(_ context.Context, coverURL string) bool {
			return !strings.Contains(coverURL, "dead.cdn")
		},
	})
	t.Cleanup(resolver.Close)

	alternates := []repository.TrackerSourceRef{
		{SourceID: 9, SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	coverURL, err := resolver.fetch(context.Background(), "primarysource", "https://primary.example/title/a", nil, alternates)
	if err != nil {
		t.Fatalf("expected the alternate's loadable cover: %v", err)
	}
	if coverURL != "https://live.cdn.example/cover.webp" {
		t.Fatalf("expected the loadable cover to win, got %q", coverURL)
	}

	// The serving source must be the one whose image loads, so the badge
	// matches the picture.
	cacheKey := coverCacheKey("primarysource", "https://primary.example/title/a", nil)
	_, servingKey, found, ok := resolver.cachedWithSource(cacheKey)
	if !ok || !found || servingKey != "mirrorsource" {
		t.Fatalf("expected the mirror recorded as serving source, got %q (found=%v ok=%v)", servingKey, found, ok)
	}
}

// TestFetchCoverURLAllCoversUnloadableCachesNegative: with every candidate's
// image host down, the failure is cached for the retry span, not served as a
// broken image.
func TestFetchCoverURLAllCoversUnloadableCachesNegative(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource", cover: "https://dead.cdn.example/cover.webp"}); err != nil {
		t.Fatalf("register primary connector: %v", err)
	}

	resolver := NewCoverResolver(CoverConfig{
		Registry:   registry,
		URLChecker: func(context.Context, string) bool { return false },
	})
	t.Cleanup(resolver.Close)

	if _, err := resolver.fetch(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error when no cover loads")
	}
	entry := negativeCoverEntry(t, resolver, coverCacheKey("primarysource", "https://primary.example/title/a", nil))
	if remaining := time.Until(entry.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the retry negative TTL, got %s", remaining)
	}
}

func TestFetchCoverURLWithoutAlternatesStillFails(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}

	resolver := newCoverTestResolver(t, registry)

	if _, err := resolver.fetch(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}
}

func negativeCoverEntry(t *testing.T, resolver *CoverResolver, cacheKey string) coverEntry {
	t.Helper()
	entry, exists := resolver.cache.get(cacheKey)
	if !exists {
		t.Fatalf("expected a negative cache entry")
	}
	if entry.Found {
		t.Fatalf("expected the entry to record a failure")
	}
	return entry
}

// TestFetchCoverURLNegativeCacheSplitsAttemptFromUnusable gives covers the same
// distinction chapter links already had. Before this, every failure was held for
// two minutes, so a page of trackers whose sources were down re-queried all of
// them every two minutes for as long as the page stayed open — against sites
// that answer sustained traffic with a bot challenge.
func TestFetchCoverURLNegativeCacheSplitsAttemptFromUnusable(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}

	resolver := newCoverTestResolver(t, registry)

	if _, err := resolver.fetch(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}
	attempted := negativeCoverEntry(t, resolver, coverCacheKey("blockedsource", "https://blocked.example/title/a", nil))
	if remaining := time.Until(attempted.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the retry negative TTL after a real attempt, got %s", remaining)
	}

	if _, err := resolver.fetch(context.Background(), "nosuchsource", "https://nowhere.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error for an unregistered connector")
	}
	unusable := negativeCoverEntry(t, resolver, coverCacheKey("nosuchsource", "https://nowhere.example/title/a", nil))
	if remaining := time.Until(unusable.ExpiresAt); remaining <= maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected a longer negative TTL when nothing was queried, got %s", remaining)
	}
}

// TestLookupQueuesAndAbandonsByPageKey pins the page gate: a lookup queued for
// the page the reader is on runs, and one queued for a page they have navigated
// away from is dropped rather than spending a connector slot on a card nobody
// is waiting for.
func TestLookupQueuesAndAbandonsByPageKey(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource", cover: "https://cdn.example/cover.webp"}); err != nil {
		t.Fatalf("register primary connector: %v", err)
	}

	gate := NewPageGate()
	resolver := NewCoverResolver(CoverConfig{
		Registry:   registry,
		Gate:       gate,
		URLChecker: func(context.Context, string) bool { return true },
	})
	t.Cleanup(resolver.Close)

	gate.SetActive("/dashboard/trackers?page=1")
	if _, _, waiting := resolver.Lookup("primarysource", "https://primary.example/title/a", nil, nil, "/dashboard/trackers?page=1"); !waiting {
		t.Fatalf("expected the first lookup to queue a fetch")
	}
	waitForCover(t, resolver, coverCacheKey("primarysource", "https://primary.example/title/a", nil))

	// The reader has moved on: a fetch for the page they left must not run.
	gate.SetActive("/dashboard/trackers?page=2")
	if _, _, waiting := resolver.Lookup("primarysource", "https://primary.example/title/b", nil, nil, "/dashboard/trackers?page=1"); !waiting {
		t.Fatalf("expected the stale-page lookup to report a pending fetch")
	}
	staleKey := coverCacheKey("primarysource", "https://primary.example/title/b", nil)
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, exists := resolver.cache.get(staleKey); exists {
			t.Fatalf("expected the abandoned page's fetch never to resolve")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForCover(t *testing.T, resolver *CoverResolver, cacheKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, found, ok := resolver.cached(cacheKey); ok && found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected a cover cache entry for %q", cacheKey)
}

// TestJitteredTTLStaysWithinItsBand pins both ends: expiry must never land below
// the span it was asked for, or the retry budget silently shrinks, and never
// above the quarter that callers assert against.
func TestJitteredTTLStaysWithinItsBand(t *testing.T) {
	const span = 10 * time.Minute
	for i := 0; i < 500; i++ {
		got := jitteredTTL(span)
		if got < span || got > maxJitteredTTL(span) {
			t.Fatalf("jitteredTTL(%s) = %s, outside [%s, %s]", span, got, span, maxJitteredTTL(span))
		}
	}
	if got := jitteredTTL(0); got != 0 {
		t.Fatalf("expected a zero span to stay zero, got %s", got)
	}
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

	resolver := newLocalCoverResolver(t, registry)

	served, err := resolver.fetch(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil)
	if err != nil {
		t.Fatalf("fetch cover: %v", err)
	}
	if !strings.HasPrefix(served, coverLocalURLPrefix) || !strings.HasSuffix(served, ".png") {
		t.Fatalf("expected a local /covers URL, got %q", served)
	}

	name := strings.TrimPrefix(served, coverLocalURLPrefix)
	if _, err := os.Stat(filepath.Join(resolver.dir, name)); err != nil {
		t.Fatalf("expected the image stored on disk: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected exactly one download, got %d", hits.Load())
	}

	// The cached entry serves the same local URL without another download,
	// and outlives any nominal remote TTL.
	cacheKey := coverCacheKey("primarysource", "https://primary.example/title/a", nil)
	cached, sourceKey, found, ok := resolver.cachedWithSource(cacheKey)
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

// TestFetchCoverURLKeepsCoverArtOfAnyShape pins that a cover is judged on being
// an image, not on its proportions. A version of this resolver refused anything
// not clearly portrait; measuring the stored library showed the shapes run from
// 1.00 to 1.58 tall-over-wide and that the squarest files are real title art, so
// the rule only ever produced cards with no art. The series that surfaced it
// publishes a 1125x675 cover on its one linked source.
func TestFetchCoverURLKeepsCoverArtOfAnyShape(t *testing.T) {
	landscape := encodeJPEG(t, 30, 18)
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(landscape)
	}))
	defer imageServer.Close()

	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "onlysource", cover: imageServer.URL + "/wide.jpg"}); err != nil {
		t.Fatalf("register connector: %v", err)
	}

	resolver := newLocalCoverResolver(t, registry)

	served, err := resolver.fetch(context.Background(), "onlysource", "https://only.example/title/a", nil, nil)
	if err != nil {
		t.Fatalf("a landscape cover on the only source must still be served: %v", err)
	}
	if !strings.HasPrefix(served, coverLocalURLPrefix) {
		t.Fatalf("expected a local /covers URL, got %q", served)
	}
	name := strings.TrimPrefix(served, coverLocalURLPrefix)
	if _, err := os.Stat(filepath.Join(resolver.dir, name)); err != nil {
		t.Fatalf("expected the image stored on disk: %v", err)
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

	resolver := newLocalCoverResolver(t, registry)

	if _, err := resolver.fetch(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected a non-image body to be rejected")
	}
	files, err := os.ReadDir(resolver.dir)
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
	resolver := newLocalCoverResolver(t, connectors.NewRegistry())

	cacheKey := coverCacheKey("primarysource", "https://primary.example/title/a", nil)
	resolver.cacheLocal(cacheKey, "https://cdn.example/cover.png", "gone.png", "primarysource")

	if _, _, ok := resolver.cached(cacheKey); ok {
		t.Fatalf("expected a missing file to read as a cache miss")
	}
	if _, stillThere := resolver.cache.get(cacheKey); stillThere {
		t.Fatalf("expected the dangling entry to be dropped")
	}
}

// TestCachedLocalCoverIgnoresExpiry pins permanence: a local entry far past
// its nominal expiry still serves, because the file on disk is the cache.
func TestCachedLocalCoverIgnoresExpiry(t *testing.T) {
	resolver := newLocalCoverResolver(t, connectors.NewRegistry())

	name := "kept.png"
	if err := os.WriteFile(filepath.Join(resolver.dir, name), pngBytes, 0o644); err != nil {
		t.Fatalf("write cover file: %v", err)
	}

	cacheKey := coverCacheKey("primarysource", "https://primary.example/title/a", nil)
	resolver.cache.put(cacheKey, coverEntry{
		CoverURL:  "https://cdn.example/cover.png",
		Found:     true,
		SourceKey: "primarysource",
		ExpiresAt: time.Now().UTC().Add(-24 * time.Hour),
		LocalPath: name,
	})

	cached, found, ok := resolver.cached(cacheKey)
	if !ok || !found || cached != coverLocalURLPrefix+name {
		t.Fatalf("expected the expired local entry to keep serving, got %q (found=%v ok=%v)", cached, found, ok)
	}

	// The background sweep must leave it alone for the same reason.
	resolver.cache.sweepExpired(time.Now().UTC())
	if _, stillThere := resolver.cache.get(cacheKey); !stillThere {
		t.Fatalf("expected the sweep to spare an entry whose file is the cache")
	}
}
