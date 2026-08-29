package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// chapterFetchLimit bounds how many chapter-link resolves run at once, so a
// page load cannot put one request per card on a site at the same moment.
const chapterFetchLimit = 10

// chapterEntry is one remembered chapter-link lookup.
type chapterEntry struct {
	ChapterURL string
	Found      bool
	ExpiresAt  time.Time
}

func (e chapterEntry) expired(now time.Time) bool {
	return now.After(e.ExpiresAt)
}

// ChapterConfig wires a ChapterLinkResolver.
type ChapterConfig struct {
	// Registry resolves a source key to the connector that reads that site, and
	// publishes the reading rank the chain below sorts by.
	Registry *connectors.Registry
	// Gate abandons a resolve for a page the reader has navigated away from.
	Gate *PageGate
}

// ChapterLinkResolver answers where a chapter opens from a cache, and on a miss
// resolves one in the background across the tracker's linked sources.
type ChapterLinkResolver struct {
	registry *connectors.Registry

	cache   *resultCache[chapterEntry]
	queue   *fetchQueue
	sem     chan struct{}
	sweeper *sweeper
}

func NewChapterLinkResolver(cfg ChapterConfig) *ChapterLinkResolver {
	resolver := &ChapterLinkResolver{
		registry: cfg.Registry,
		cache:    newResultCache[chapterEntry](),
		queue:    newFetchQueue(cfg.Gate),
		sem:      make(chan struct{}, chapterFetchLimit),
	}
	resolver.sweeper = startSweeper(sweepInterval, func() {
		resolver.cache.sweepExpired(time.Now().UTC())
	})
	return resolver
}

// Close stops the background expiry sweep.
func (r *ChapterLinkResolver) Close() {
	r.sweeper.Close()
}

// Lookup returns a chapter's reader URL, whether that URL is a resolved chapter
// link rather than the series page it degrades to, and whether a background
// resolve is still pending. The caller needs the middle value to tell "this link
// opens chapter 65 on some site" from "we gave up and pointed at the series
// page", because only the former says which site is serving the card.
func (r *ChapterLinkResolver) Lookup(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, pageKey string) (chapterURL string, resolved bool, waiting bool) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", false, false
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, false, false
	}

	cacheKey := chapterCacheKey(trimmedSourceKey, trimmedSourceURL, chapter)
	if cachedChapterURL, found, ok := r.cached(cacheKey); ok {
		if found {
			return cachedChapterURL, true, false
		}
		return trimmedSourceURL, false, false
	}

	r.queue.run(cacheKey, r.sem, pageKey, func() {
		_, _ = r.fetch(trimmedSourceKey, trimmedSourceURL, chapter, alternates)
	})
	return trimmedSourceURL, false, true
}

// Invalidate drops every remembered chapter link. Called whenever a tracker's
// linked sources or reading pin change: a "no link found" computed before a
// source was attached would otherwise outlive the attachment by its whole TTL,
// which reads as the new link not working.
func (r *ChapterLinkResolver) Invalidate() {
	r.cache.reset()
}

// fetch resolves a chapter's reader URL across a tracker's linked sources in
// three tiers, so a blocked site does not leave every chapter link pointing
// nowhere useful. First, every readable site in reading-priority order (see
// orderReaderCandidates) gets to verify it carries the chapter — a resolver
// that answers ErrChapterNotFound has answered, and cedes its turn for this
// chapter. Second, a site that could not be asked at all but can construct its
// reader URL offline (MangaFire behind its challenge) serves the built link —
// the reader's own browser passes the challenge the server cannot, so an
// unverified link there beats a verified one on the floor. Third, the info-floor
// sites (ComicK): they always carry the chapter page, but nobody wants to read
// there, so they only serve when nothing else could.
func (r *ChapterLinkResolver) fetch(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef) (string, error) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", fmt.Errorf("missing source url")
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, nil
	}

	cacheKey := chapterCacheKey(trimmedSourceKey, trimmedSourceURL, chapter)
	if cachedChapterURL, found, ok := r.cached(cacheKey); ok {
		if found {
			return cachedChapterURL, nil
		}
		return trimmedSourceURL, fmt.Errorf("chapter url not found")
	}

	// Primary source first, then the tracker's other linked sources, reordered
	// by reading priority.
	candidates := make([]repository.TrackerSourceRef, 0, len(alternates)+1)
	candidates = append(candidates, repository.TrackerSourceRef{SourceKey: trimmedSourceKey, SourceURL: trimmedSourceURL})
	candidates = append(candidates, alternates...)
	orderReaderCandidates(candidates, r.readerCandidateRank)

	// A source that was actually queried and failed may succeed shortly; one with
	// no usable connector will not, so the two are cached for different spans.
	attempted := false
	var lastErr error

	// Sites whose resolver could not be asked (as opposed to answering "not
	// carried") and that can build their reader URL offline, in chain order.
	type blockedLinkable struct {
		linker connectors.OfflineChapterLinker
		url    string
	}
	blocked := make([]blockedLinkable, 0, 1)
	infoFloor := make([]repository.TrackerSourceRef, 0, 1)

	resolveCandidate := func(candidateKey, candidateURL string) (string, bool) {
		connector, ok := r.connectorForKey(candidateKey)
		if !ok {
			lastErr = fmt.Errorf("connector not found")
			return "", false
		}

		resolver, ok := connector.(connectors.ChapterURLResolver)
		if !ok {
			lastErr = fmt.Errorf("chapter resolver not supported")
			return "", false
		}

		attempted = true
		chapterURL, err := resolveChapterURLFromConnector(resolver, candidateURL, chapter)
		switch {
		case err != nil:
			lastErr = err
			// The site answered "I do not carry this chapter": its turn is
			// over, a built link would point at a page known not to exist.
			// Any other failure means the site never answered, so its
			// offline-built link may still claim a turn in the second tier.
			if !errors.Is(err, connectors.ErrChapterNotFound) {
				if linker, ok := connector.(connectors.OfflineChapterLinker); ok {
					blocked = append(blocked, blockedLinkable{linker: linker, url: candidateURL})
				}
			}
		case chapterURL == "":
			lastErr = fmt.Errorf("chapter url empty")
		default:
			return chapterURL, true
		}
		return "", false
	}

	// Tier 1: readable sites that verify they carry the chapter.
	for _, candidate := range candidates {
		candidateKey := strings.TrimSpace(candidate.SourceKey)
		candidateURL := strings.TrimSpace(candidate.SourceURL)
		if candidateKey == "" || candidateURL == "" {
			continue
		}
		if r.readerCandidateRank(candidateKey) == connectors.ReaderRankInfoFloor {
			infoFloor = append(infoFloor, candidate)
			continue
		}

		if chapterURL, ok := resolveCandidate(candidateKey, candidateURL); ok {
			r.cacheResult(cacheKey, chapterURL, true, 12*time.Hour)
			return chapterURL, nil
		}
	}

	// Tier 2: offline-built links from sites that could not be asked. Cached
	// for the retry span, not the full 12 hours: once the site answers the
	// server again, a verified resolution should replace the guess soon
	// rather than a day later.
	for _, candidate := range blocked {
		if built, buildOK := candidate.linker.BuildChapterURL(candidate.url, chapter); buildOK && strings.TrimSpace(built) != "" {
			r.cacheResult(cacheKey, built, true, jitteredTTL(lookupRetryTTL))
			return built, nil
		}
	}

	// Tier 3: the info floor — typically the site that reported the chapter
	// number in the first place, so at least its chapter page exists.
	for _, candidate := range infoFloor {
		candidateKey := strings.TrimSpace(candidate.SourceKey)
		candidateURL := strings.TrimSpace(candidate.SourceURL)
		if candidateKey == "" || candidateURL == "" {
			continue
		}
		if chapterURL, ok := resolveCandidate(candidateKey, candidateURL); ok {
			r.cacheResult(cacheKey, chapterURL, true, 12*time.Hour)
			return chapterURL, nil
		}
	}

	negativeTTL := lookupUnreachableTTL
	if attempted {
		negativeTTL = lookupRetryTTL
	}
	r.cacheResult(cacheKey, "", false, jitteredTTL(negativeTTL))
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable source")
	}
	return trimmedSourceURL, fmt.Errorf("resolve chapter url: %w", lastErr)
}

func resolveChapterURLFromConnector(resolver connectors.ChapterURLResolver, sourceURL string, chapter float64) (string, error) {
	// Background work behind the shared per-host throttle: see the cover
	// resolve timeout for why this is generous.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	chapterURL, err := resolver.ResolveChapterURL(ctx, sourceURL, chapter)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(chapterURL), nil
}

// readerCandidateRank orders a tracker's linked sources for chapter-link
// resolution when no reading pin narrows the choice. Each site's tier is the
// connector's own (SiteInfo.ReaderRank), so the chain follows the site the
// connector says it reads rather than a second table here that could disagree
// with it. Origin scanlator sites go first: for their own series they are where
// chapters appear before any aggregator mirrors them, and their readers are the
// best of the chain. Then the fresh aggregators, then the remaining reader
// sites in their incoming order. The info floor ranks last: those sites always
// have the chapter page, which makes them the reliable fallback, but their
// readers are the worst of the chain — see fetch for how the floor is only
// reached after every readable site and every offline-built link had its turn.
// A source with no connector, or one that publishes no metadata, keeps the
// default tier it has always had.
func (r *ChapterLinkResolver) readerCandidateRank(sourceKey string) int {
	connector, ok := r.connectorForKey(sourceKey)
	if !ok {
		return connectors.ReaderRankDefault
	}
	info, ok := connector.(connectors.SiteInfo)
	if !ok {
		return connectors.ReaderRankDefault
	}
	return info.ReaderRank()
}

// orderReaderCandidates reorders candidates in place by the rank function,
// keeping the incoming order (primary first, then linked alternates) between
// sources of equal rank. The ranking is passed in so the ordering can be
// exercised against any rank table, the resolver's included.
func orderReaderCandidates(candidates []repository.TrackerSourceRef, rank func(sourceKey string) int) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return rank(candidates[i].SourceKey) < rank(candidates[j].SourceKey)
	})
}

// connectorForKey resolves a source key through the registry. A resolver built
// without one answers "no connector" rather than dereferencing nil: this runs
// in a background goroutine, where a panic takes the whole process down instead
// of one request.
func (r *ChapterLinkResolver) connectorForKey(sourceKey string) (connectors.Connector, bool) {
	if r.registry == nil {
		return nil, false
	}
	return r.registry.Get(strings.TrimSpace(sourceKey))
}

func chapterCacheKey(sourceKey, sourceURL string, chapter float64) string {
	return strings.ToLower(strings.TrimSpace(sourceKey)) + "|" + strings.ToLower(strings.TrimSpace(sourceURL)) + "|" + strconv.FormatFloat(chapter, 'f', -1, 64)
}

func (r *ChapterLinkResolver) cached(cacheKey string) (chapterURL string, found bool, ok bool) {
	entry, exists := r.cache.get(cacheKey)
	if !exists {
		return "", false, false
	}

	if entry.expired(time.Now().UTC()) {
		r.cache.drop(cacheKey)
		return "", false, false
	}

	return entry.ChapterURL, entry.Found, true
}

func (r *ChapterLinkResolver) cacheResult(cacheKey, chapterURL string, found bool, ttl time.Duration) {
	r.cache.put(cacheKey, chapterEntry{
		ChapterURL: chapterURL,
		Found:      found,
		ExpiresAt:  time.Now().UTC().Add(ttl),
	})
}
