package resolve

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// coverLocalURLPrefix is where the router serves the files under the resolver's
// directory. The two have to agree: a name minted here is handed straight to
// the browser as an href.
const coverLocalURLPrefix = "/covers/"

// coverLocalTTL is the nominal expiry stamped on entries whose image lives on
// disk. They are exempt from every expiry check — the file is the cache and
// lives as long as the tracker — so the value only keeps the column non-null.
const coverLocalTTL = 10 * 365 * 24 * time.Hour

// coverFetchLimit bounds how many cover fetches run at once. MangaFire gets a
// smaller budget of its own so a library dominated by MangaFire cards cannot
// spend the whole pool on the one site that answers parallel traffic with a bot
// challenge.
const (
	coverFetchLimit     = 8
	mangafireCoverLimit = 3
)

// coverEntry is one remembered cover lookup.
type coverEntry struct {
	CoverURL string
	Found    bool
	// SourceKey names the source that actually supplied the cover, which is not
	// always the tracker's primary one. The card badge and its "open" link follow
	// it so the UI never claims a site that served nothing.
	SourceKey string
	ExpiresAt time.Time
	// LocalPath names the downloaded copy under the resolver's directory.
	// Non-empty entries serve /covers/{LocalPath} and never expire — the file
	// is the cache.
	LocalPath string
}

func (e coverEntry) expired(now time.Time) bool {
	return e.LocalPath == "" && now.After(e.ExpiresAt)
}

// coverStore is what the resolver needs of the persistent cover cache:
// repository.CoverCacheRepository in the running app, anything that answers
// the same four calls in a test.
type coverStore interface {
	LoadFreshContext(ctx context.Context) ([]repository.CoverCacheRow, error)
	UpsertContext(ctx context.Context, entry repository.CoverCacheRow) error
	DeleteContext(ctx context.Context, cacheKey string) error
	DeleteNegativesContext(ctx context.Context) error
}

// CoverConfig wires a CoverResolver. Everything it reaches — the connectors,
// the persistent store, the directory, the HTTP client — is handed in, so none
// of this has to be driven through a running server to be exercised.
type CoverConfig struct {
	// Registry resolves a source key to the connector that reads that site.
	Registry *connectors.Registry
	// Store persists cover entries across restarts (nil keeps the cache in
	// memory only). The map stays the hot path; the store is write-through and
	// only read once, at construction.
	Store coverStore
	// Dir is where resolved covers are downloaded and served from (/covers).
	// Empty disables the local store and falls back to hotlinking the source
	// CDNs — the pre-store behavior.
	Dir string
	// Client fetches candidate cover images. Nil installs the guarded client:
	// cover URLs come from scraped third-party pages, so the default has to be
	// the one that refuses anything but https to a public address.
	Client *http.Client
	// URLChecker reports whether a cover image URL actually answers, standing in
	// for the download and the probe. It exists because a connector can resolve a
	// syntactically fine cover URL whose CDN is dead (ComicK's meo host,
	// 2026-08), and caching that URL for 12h leaves a card broken with working
	// alternates one hop away.
	URLChecker func(ctx context.Context, coverURL string) bool
	// Gate abandons a fetch for a page the reader has navigated away from.
	Gate *PageGate
}

// CoverResolver answers a tracker's cover art from a cache, and on a miss
// resolves one in the background across the tracker's linked sources.
type CoverResolver struct {
	registry   *connectors.Registry
	store      coverStore
	dir        string
	client     *http.Client
	urlChecker func(ctx context.Context, coverURL string) bool

	cache        *resultCache[coverEntry]
	queue        *fetchQueue
	sem          chan struct{}
	mangafireSem chan struct{}
	sweeper      *sweeper
}

func NewCoverResolver(cfg CoverConfig) *CoverResolver {
	client := cfg.Client
	if client == nil {
		client = guardedCoverClient
	}

	resolver := &CoverResolver{
		registry:     cfg.Registry,
		store:        cfg.Store,
		dir:          strings.TrimSpace(cfg.Dir),
		client:       client,
		urlChecker:   cfg.URLChecker,
		cache:        newResultCache[coverEntry](),
		queue:        newFetchQueue(cfg.Gate),
		sem:          make(chan struct{}, coverFetchLimit),
		mangafireSem: make(chan struct{}, mangafireCoverLimit),
	}

	// A directory that cannot be created only costs the local copies: covers
	// degrade to hotlinking the source CDNs, the pre-store behavior.
	if resolver.dir != "" {
		if err := os.MkdirAll(resolver.dir, 0o755); err != nil {
			slog.Warn("cover directory unavailable; serving covers remotely", "dir", resolver.dir, "error", err)
			resolver.dir = ""
		}
	}

	resolver.seedFromStore()
	resolver.sweeper = startSweeper(sweepInterval, func() {
		resolver.cache.sweepExpired(time.Now().UTC())
	})
	return resolver
}

// Close cancels the background fetches, waits briefly for them to return, and
// stops the expiry sweep. After it nothing here touches the store or the
// network again, which is what lets the caller close the database.
func (r *CoverResolver) Close() {
	r.queue.close()
	r.sweeper.Close()
}

// Lookup returns the cover URL, the source key that supplied it, and whether a
// background fetch is still pending. The serving source is empty until a fetch
// completes, so a first render shows the tracker's primary source and a later
// one corrects it if a fallback answered instead.
func (r *CoverResolver) Lookup(sourceKey, sourceURL string, sourceItemID *string, alternates []repository.TrackerSourceRef, pageKey string) (coverURL string, servingSourceKey string, waiting bool) {
	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return "", "", false
	}

	cacheKey := coverCacheKey(trimmedSourceKey, sourceURL, sourceItemID)
	if cachedURL, servingKey, found, ok := r.cachedWithSource(cacheKey); ok {
		if found {
			return cachedURL, servingKey, false
		}
		return "", "", false
	}

	if strings.TrimSpace(sourceURL) == "" {
		r.cacheResult(cacheKey, "", false, jitteredTTL(lookupUnreachableTTL))
		return "", "", false
	}

	r.queue.run(cacheKey, r.semFor(trimmedSourceKey), pageKey, func(ctx context.Context) {
		_, _ = r.fetch(ctx, trimmedSourceKey, sourceURL, sourceItemID, alternates)
	})
	return "", "", true
}

// InvalidateNegatives drops every remembered "no cover found". Called whenever
// a tracker's linked sources change: an entry computed before a source was
// attached would otherwise outlive the attachment by its whole TTL, which reads
// as the new link not working. Found covers stay — a new link cannot make a
// good cover worse.
func (r *CoverResolver) InvalidateNegatives() {
	r.cache.dropWhere(func(entry coverEntry) bool { return !entry.Found })

	if r.store != nil {
		if err := r.store.DeleteNegativesContext(r.queue.lifetime); err != nil {
			slog.Debug("cover cache negative sweep failed", "error", err)
		}
	}
}

// semFor picks the budget a fetch waits in. MangaFire's is separate so its
// per-request signer work cannot starve every other site's covers.
func (r *CoverResolver) semFor(sourceKey string) chan struct{} {
	if strings.EqualFold(strings.TrimSpace(sourceKey), "mangafire") {
		return r.mangafireSem
	}
	return r.sem
}

// fetch resolves a cover for a tracker, trying its primary source first and
// then each alternate linked source. The result is cached under the primary
// key either way, so a cover found on a mirror still serves the tracker whose
// primary site is unreachable.
func (r *CoverResolver) fetch(parent context.Context, sourceKey, sourceURL string, sourceItemID *string, alternates []repository.TrackerSourceRef) (string, error) {
	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return "", fmt.Errorf("missing source key")
	}

	cacheKey := coverCacheKey(trimmedSourceKey, sourceURL, sourceItemID)
	if cachedURL, found, ok := r.cached(cacheKey); ok {
		if found {
			return cachedURL, nil
		}
		return "", fmt.Errorf("cover not found")
	}

	resolvedURL := strings.TrimSpace(sourceURL)
	if resolvedURL == "" {
		r.cacheResult(cacheKey, "", false, jitteredTTL(lookupUnreachableTTL))
		return "", fmt.Errorf("missing source url")
	}

	tryKeys := make([]string, 0, 2)
	tryKeys = append(tryKeys, trimmedSourceKey)

	if fallbackKey := r.sourceKeyForURL(resolvedURL); fallbackKey != "" && fallbackKey != trimmedSourceKey {
		tryKeys = append(tryKeys, fallbackKey)
	}

	// A source that was actually queried and failed may succeed shortly; one with
	// no usable connector will not, so the two are cached for different spans —
	// the same split the chapter-link resolver makes.
	attempted := false

	for _, key := range tryKeys {
		coverURL, tried, err := r.resolveFromConnector(parent, key, resolvedURL)
		attempted = attempted || tried
		if err != nil || coverURL == "" {
			continue
		}
		if served, ok := r.acceptCover(parent, cacheKey, coverURL, trimmedSourceKey); ok {
			return served, nil
		}
	}

	// The primary source could not supply a cover. Fall back to the tracker's
	// other linked sources, which is what keeps a library readable when a site
	// goes behind a bot challenge.
	for _, alternate := range alternates {
		alternateURL := strings.TrimSpace(alternate.SourceURL)
		alternateKey := strings.TrimSpace(alternate.SourceKey)
		if alternateURL == "" || alternateKey == "" {
			continue
		}

		coverURL, tried, err := r.resolveFromConnector(parent, alternateKey, alternateURL)
		attempted = attempted || tried
		if err != nil || coverURL == "" {
			continue
		}
		if served, ok := r.acceptCover(parent, cacheKey, coverURL, alternateKey); ok {
			return served, nil
		}
	}

	negativeTTL := lookupUnreachableTTL
	if attempted {
		negativeTTL = lookupRetryTTL
	}
	r.cacheResult(cacheKey, "", false, jitteredTTL(negativeTTL))
	return "", fmt.Errorf("cover not found")
}

// acceptCover validates a resolved cover URL and caches it, returning the URL
// the card should serve. With a local directory configured the image is
// downloaded once and served from this host from then on — the download doubles
// as the "does this URL actually load" probe. Without one (a caller injects a
// checker; the directory can fail at startup) it degrades to the old behavior:
// probe one byte and hotlink the source CDN for the TTL.
func (r *CoverResolver) acceptCover(parent context.Context, cacheKey, coverURL, sourceKey string) (string, bool) {
	if r.urlChecker != nil {
		if !r.urlChecker(parent, coverURL) {
			return "", false
		}
	} else if r.dir != "" {
		localPath, ok := r.storeLocally(parent, coverURL)
		if !ok {
			return "", false
		}
		r.cacheLocal(cacheKey, coverURL, localPath, sourceKey)
		return coverLocalURLPrefix + localPath, true
	} else if !r.probe(parent, coverURL) {
		return "", false
	}

	r.cacheResultFromSource(cacheKey, coverURL, sourceKey, true, 12*time.Hour)
	return coverURL, true
}

// resolveFromConnector also reports whether a connector was actually queried.
// "No connector registered for this key" and "the site refused us" are both
// failures here, but only the second is worth retrying soon.
func (r *CoverResolver) resolveFromConnector(parent context.Context, sourceKey, sourceURL string) (string, bool, error) {
	connector, ok := r.connectorForKey(sourceKey)
	if !ok {
		return "", false, fmt.Errorf("connector not found")
	}

	// Generous because these fetches run in background goroutines and the
	// shared throttle makes a page-load's worth of them queue single-file per
	// host: the last of two dozen covers legitimately waits tens of seconds
	// for its slot, and cutting it down just caches "no cover" for ten
	// minutes.
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, sourceURL)
	if err != nil {
		return "", true, err
	}
	if result == nil {
		return "", true, fmt.Errorf("empty result")
	}

	return strings.TrimSpace(result.CoverImageURL), true, nil
}

// connectorForKey resolves a source key through the registry. A resolver built
// without one answers "no connector" rather than dereferencing nil: this runs
// in a background goroutine, where a panic takes the whole process down instead
// of one request.
func (r *CoverResolver) connectorForKey(sourceKey string) (connectors.Connector, bool) {
	if r.registry == nil {
		return nil, false
	}
	return r.registry.Get(strings.TrimSpace(sourceKey))
}

// sourceKeyForURL names the site a URL belongs to, through the hosts the
// connectors themselves publish.
func (r *CoverResolver) sourceKeyForURL(rawURL string) string {
	if r.registry == nil {
		return ""
	}
	connector, ok := r.registry.GetByURL(rawURL)
	if !ok {
		return ""
	}
	return connector.Key()
}

func coverCacheKey(sourceKey, sourceURL string, sourceItemID *string) string {
	itemID := ""
	if sourceItemID != nil {
		itemID = strings.TrimSpace(*sourceItemID)
	}

	base := strings.ToLower(strings.TrimSpace(sourceKey)) + "|"
	if itemID != "" {
		return base + "item:" + strings.ToLower(itemID)
	}

	trimmedURL := strings.TrimSpace(sourceURL)
	if trimmedURL != "" {
		return base + "url:" + strings.ToLower(trimmedURL)
	}

	return base + "missing"
}

func (r *CoverResolver) cached(cacheKey string) (coverURL string, found bool, ok bool) {
	coverURL, _, found, ok = r.cachedWithSource(cacheKey)
	return coverURL, found, ok
}

// cachedWithSource also reports which source supplied the cached cover. An
// entry with a local copy never expires, but it is only as good as its file:
// one whose file vanished is dropped so the next render re-fetches the cover.
func (r *CoverResolver) cachedWithSource(cacheKey string) (coverURL string, sourceKey string, found bool, ok bool) {
	entry, exists := r.cache.get(cacheKey)
	if !exists {
		return "", "", false, false
	}

	if entry.LocalPath != "" {
		if _, err := os.Stat(filepath.Join(r.dir, entry.LocalPath)); err == nil {
			return coverLocalURLPrefix + entry.LocalPath, entry.SourceKey, true, true
		}
		lostPath := entry.LocalPath
		r.dropCached(cacheKey, func(current coverEntry) bool { return current.LocalPath == lostPath })
		return "", "", false, false
	}

	if now := time.Now().UTC(); entry.expired(now) {
		r.dropCached(cacheKey, func(current coverEntry) bool { return current.expired(now) })
		return "", "", false, false
	}

	return entry.CoverURL, entry.SourceKey, entry.Found, true
}

// dropCached evicts an entry from the cache and the store, but only while
// stale still describes what the cache holds: a fetch that landed a fresh
// answer between the read and this call keeps it.
func (r *CoverResolver) dropCached(cacheKey string, stale func(coverEntry) bool) {
	if !r.cache.dropIf(cacheKey, stale) {
		return
	}
	if r.store != nil {
		if err := r.store.DeleteContext(r.queue.lifetime, cacheKey); err != nil {
			slog.Debug("cover cache delete failed", "error", err)
		}
	}
}

func (r *CoverResolver) cacheResult(cacheKey, coverURL string, found bool, ttl time.Duration) {
	r.cacheResultFromSource(cacheKey, coverURL, "", found, ttl)
}

func (r *CoverResolver) cacheResultFromSource(cacheKey, coverURL, sourceKey string, found bool, ttl time.Duration) {
	r.putEntry(cacheKey, coverEntry{
		CoverURL:  coverURL,
		Found:     found,
		SourceKey: strings.TrimSpace(sourceKey),
		ExpiresAt: time.Now().UTC().Add(ttl),
	})
}

// cacheLocal records a cover whose image now lives on disk. The remote URL is
// kept alongside for reference, but the entry serves the local copy and is
// exempt from expiry.
func (r *CoverResolver) cacheLocal(cacheKey, coverURL, localPath, sourceKey string) {
	r.putEntry(cacheKey, coverEntry{
		CoverURL:  coverURL,
		Found:     true,
		SourceKey: strings.TrimSpace(sourceKey),
		ExpiresAt: time.Now().UTC().Add(coverLocalTTL),
		LocalPath: localPath,
	})
}

func (r *CoverResolver) putEntry(cacheKey string, entry coverEntry) {
	r.cache.put(cacheKey, entry)

	// Write-through so the entry survives restarts; best-effort because a
	// failed persist only costs a re-resolve after the next restart.
	if r.store != nil {
		if err := r.store.UpsertContext(r.queue.lifetime, repository.CoverCacheRow{
			CacheKey:  cacheKey,
			CoverURL:  entry.CoverURL,
			SourceKey: entry.SourceKey,
			Found:     entry.Found,
			ExpiresAt: entry.ExpiresAt,
			LocalPath: entry.LocalPath,
		}); err != nil {
			slog.Debug("cover cache persist failed", "error", err)
		}
	}
}

// seedFromStore warms the in-memory cache from the persisted one. A failure
// only costs the warm start, so it is logged and swallowed.
func (r *CoverResolver) seedFromStore() {
	if r.store == nil {
		return
	}
	entries, err := r.store.LoadFreshContext(r.queue.lifetime)
	if err != nil {
		slog.Warn("cover cache load failed; starting cold", "error", err)
		return
	}
	for _, entry := range entries {
		r.cache.put(entry.CacheKey, coverEntry{
			CoverURL:  entry.CoverURL,
			Found:     entry.Found,
			SourceKey: entry.SourceKey,
			ExpiresAt: entry.ExpiresAt,
			LocalPath: entry.LocalPath,
		})
	}
	if len(entries) > 0 {
		slog.Info("cover cache warmed from store", "entries", len(entries))
	}
}
