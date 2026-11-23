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

// verifiedChapterTTL is how long a chapter link a site confirmed is remembered.
// A chapter's reader URL does not change once the page exists, so this is long:
// what bounds it is the site renumbering or re-slugging the chapter, not the
// answer going stale.
const verifiedChapterTTL = 12 * time.Hour

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

// Close cancels the background resolves, waits briefly for them to return, and
// stops the expiry sweep.
func (r *ChapterLinkResolver) Close() {
	r.queue.close()
	r.sweeper.Close()
}

// Lookup returns a chapter's reader URL, whether that URL is a resolved chapter
// link rather than the series page it degrades to, and whether a background
// resolve is still pending. The caller needs the middle value to tell "this link
// opens chapter 65 on some site" from "we gave up and pointed at the series
// page", because only the former says which site is serving the card.
func (r *ChapterLinkResolver) Lookup(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, pageKey string) (chapterURL string, resolved bool, waiting bool) {
	return r.LookupPreferring("", sourceKey, sourceURL, chapter, alternates, pageKey)
}

// LookupPreferring is Lookup with one of the tracker's sites asked ahead of the
// reading chain's own order. A card's two chapter links are separate questions,
// and the chain answers each with the best-ranked site that carries that
// chapter — so a chapter hours old opened on the one site that had it while the
// chapter the reader is actually on opened on a better-ranked site that did
// not, under a badge naming only the first. The card passes the site its
// latest-chapter link was confirmed on, and the older chapter is asked there
// first: that site demonstrably carries the run up to the newest chapter, which
// the better-ranked one would have stopped short of.
//
// It is a preference, not a pin: a preferred site that does not carry the
// chapter, or cannot be asked, cedes to the rest of the chain as ranked. See
// readingChain for the preferences that are ignored.
func (r *ChapterLinkResolver) LookupPreferring(preferKey, sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, pageKey string) (chapterURL string, resolved bool, waiting bool) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", false, false
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, false, false
	}

	_, preferred := r.readingChain(preferKey, trimmedSourceKey, trimmedSourceURL, alternates)
	cacheKey := preferringCacheKey(preferred, trimmedSourceKey, trimmedSourceURL, chapter, alternates)
	if cachedChapterURL, found, ok := r.cached(cacheKey); ok {
		if found {
			return cachedChapterURL, true, false
		}
		return trimmedSourceURL, false, false
	}

	// Captured now rather than when the resolve gets its slot: an invalidation
	// that lands while it waits in the queue voids its answer just the same.
	generation := r.cache.currentGeneration(chapterScope(trimmedSourceKey, trimmedSourceURL))
	r.queue.run(cacheKey, r.sem, pageKey, func(ctx context.Context) {
		_, _ = r.fetchPreferring(ctx, generation, preferred, trimmedSourceKey, trimmedSourceURL, chapter, alternates)
	})
	return trimmedSourceURL, false, true
}

// readingChain lists a tracker's usable links in the order the chain asks them:
// the primary and its alternates by reading rank, with the preferred site, when
// the preference counts, moved to the head. It also returns the preference that
// took effect, which is what a lookup is cached under — empty whenever the
// chain is the ranked one anyway, so a preference that changes nothing shares
// the plain lookup's answer instead of sending the sites a second resolve for
// it. A preference does not count when:
//   - it names the info floor: that site holds a chapter minutes old before
//     anyone else, and following it would pin the reader's whole backlog to the
//     worst reader in the chain;
//   - it names no link the tracker carries;
//   - the site already heads the ranked chain.
func (r *ChapterLinkResolver) readingChain(preferKey, sourceKey, sourceURL string, alternates []repository.TrackerSourceRef) ([]repository.TrackerSourceRef, string) {
	candidates := make([]repository.TrackerSourceRef, 0, len(alternates)+1)
	candidates = append(candidates, repository.TrackerSourceRef{SourceKey: sourceKey, SourceURL: sourceURL})
	for _, alternate := range alternates {
		alternate.SourceKey = strings.TrimSpace(alternate.SourceKey)
		alternate.SourceURL = strings.TrimSpace(alternate.SourceURL)
		if alternate.SourceKey == "" || alternate.SourceURL == "" {
			continue
		}
		candidates = append(candidates, alternate)
	}
	orderReaderCandidates(candidates, r.readerCandidateRank)

	preferred := strings.ToLower(strings.TrimSpace(preferKey))
	if preferred == "" || r.readerCandidateRank(preferred) == connectors.ReaderRankInfoFloor {
		return candidates, ""
	}
	for index, candidate := range candidates {
		if !strings.EqualFold(candidate.SourceKey, preferred) {
			continue
		}
		if index == 0 {
			return candidates, ""
		}
		copy(candidates[1:index+1], candidates[:index])
		candidates[0] = candidate
		return candidates, preferred
	}
	return candidates, ""
}

// InvalidateSource drops the chapter links remembered for lookups that start
// from one link — the source and series URL a card's chapter lookups are made
// with, which is the tracker's primary or the source its reading pin names.
// Called for each of a tracker's links when they or its pin change: a "no
// link found" computed before a source was attached would otherwise outlive
// the attachment by its whole TTL, which reads as the new link not working. A
// resolve already queued or in flight for that link was working from the
// links as they were, so it no longer writes its answer either (see fetch's
// generation).
//
// Every other tracker's links stay. This used to drop the whole cache: one
// tracker edited, and the next render of the list sent two resolves per card
// back to the sites for the entire library.
func (r *ChapterLinkResolver) InvalidateSource(sourceKey, sourceURL string) {
	scope := chapterScope(sourceKey, sourceURL)
	if scope == "" {
		return
	}
	r.cache.invalidateScope(scope, func(key string, _ chapterEntry) bool {
		return strings.HasPrefix(key, scope)
	})
}

// chapterScope names the lookups that start from one link, and is the prefix
// their cache keys share (see chapterCacheKey): the source, the series URL,
// and the separator that ends the URL. Empty for a link that names neither.
func chapterScope(sourceKey, sourceURL string) string {
	key := strings.ToLower(strings.TrimSpace(sourceKey))
	seriesURL := strings.ToLower(strings.TrimSpace(sourceURL))
	if key == "" || seriesURL == "" {
		return ""
	}
	return key + "|" + seriesURL + "|"
}

// fetch resolves a chapter's reader URL across a tracker's linked sources in
// three tiers, so a blocked site does not leave every chapter link pointing
// nowhere useful. First, every readable site in reading-priority order (see
// orderReaderCandidates) gets to verify it carries the chapter — a resolver
// that answers ErrChapterNotFound has answered, and cedes its turn for this
// chapter. Second, a site that could not be asked at all but can construct its
// reader URL offline serves the built link (no connector does today: MangaFire
// did until its site dropped the derivable reader scheme, and
// connectors.OfflineChapterLinker records what makes a site eligible) —
// the reader's own browser passes the challenge the server cannot, so an
// unverified link there beats a verified one on the floor. Third, the info-floor
// sites (ComicK): they always carry the chapter page, but nobody wants to read
// there, so they only serve when nothing else could.
//
// generation is the link's invalidation count when the resolve was queued. The
// answer is only written while it still holds; see InvalidateSource.
func (r *ChapterLinkResolver) fetch(ctx context.Context, generation uint64, sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef) (string, error) {
	return r.fetchPreferring(ctx, generation, "", sourceKey, sourceURL, chapter, alternates)
}

// fetchPreferring is fetch with one site asked ahead of the ranked chain; see
// LookupPreferring for why a card asks that, and readingChain for when the
// preference is ignored.
func (r *ChapterLinkResolver) fetchPreferring(ctx context.Context, generation uint64, preferKey, sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef) (string, error) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", fmt.Errorf("missing source url")
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, nil
	}

	// Primary source first, then the tracker's other linked sources, reordered
	// by reading priority, and the preferred site ahead of that when it counts.
	candidates, preferred := r.readingChain(preferKey, trimmedSourceKey, trimmedSourceURL, alternates)

	cacheKey := preferringCacheKey(preferred, trimmedSourceKey, trimmedSourceURL, chapter, alternates)
	if cachedChapterURL, found, ok := r.cached(cacheKey); ok {
		if found {
			return cachedChapterURL, nil
		}
		return trimmedSourceURL, fmt.Errorf("chapter url not found")
	}

	// The link was invalidated while this resolve waited for its slot, so
	// nothing it finds could be written. Asking the sites anyway would spend
	// requests on an answer that is thrown away; the card's next render queues
	// one that counts.
	scope := chapterScope(trimmedSourceKey, trimmedSourceURL)
	if r.cache.currentGeneration(scope) != generation {
		return trimmedSourceURL, fmt.Errorf("chapter links invalidated while queued")
	}

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
		chapterURL, err := resolveChapterURLFromConnector(ctx, resolver, candidateURL, chapter)
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
			r.cacheResult(generation, scope, cacheKey, chapterURL, true, verifiedChapterTTL)
			return chapterURL, nil
		}
	}

	// Tier 2: offline-built links from sites that could not be asked. Cached
	// for the retry span, not the full 12 hours: once the site answers the
	// server again, a verified resolution should replace the guess soon
	// rather than a day later.
	for _, candidate := range blocked {
		if built, buildOK := candidate.linker.BuildChapterURL(candidate.url, chapter); buildOK && strings.TrimSpace(built) != "" {
			r.cacheResult(generation, scope, cacheKey, built, true, jitteredTTL(lookupRetryTTL))
			return built, nil
		}
	}

	// A floor answer reached past a site that was asked and merely did not carry
	// the chapter *yet* is provisional: that is what a chapter minutes old looks
	// like, and the better site indexes it within the hour. Remembering it for
	// the verified span would pin a whole day of reading to the worst site in the
	// chain — the same reason tier 2 holds its built link only for the retry
	// span. Captured before the floor's own resolve, which sets the flag too.
	// With nothing better ever asked — a tracker carrying only floor sources —
	// there is no better answer coming, so that one keeps the verified span.
	floorTTL := verifiedChapterTTL
	if attempted {
		floorTTL = jitteredTTL(lookupRetryTTL)
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
			r.cacheResult(generation, scope, cacheKey, chapterURL, true, floorTTL)
			return chapterURL, nil
		}
	}

	negativeTTL := lookupUnreachableTTL
	if attempted {
		negativeTTL = lookupRetryTTL
	}
	r.cacheResult(generation, scope, cacheKey, "", false, jitteredTTL(negativeTTL))
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable source")
	}
	return trimmedSourceURL, fmt.Errorf("resolve chapter url: %w", lastErr)
}

func resolveChapterURLFromConnector(parent context.Context, resolver connectors.ChapterURLResolver, sourceURL string, chapter float64) (string, error) {
	// Background work behind the shared per-host throttle: see the cover
	// resolve timeout for why this is generous.
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
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

// chapterCacheKey names one chapter-link question by everything its answer
// depends on: the primary, the chapter, and the alternates the chain picks
// from. Keyed on the primary alone, one question's answer served another: a
// pin to the primary (which passes no alternates) read the auto chain's
// MangaHub link, and a profile tracking the same primary URL without Asura
// linked handed its ComicK floor answer to the profile that has Asura, for the
// floor's whole 12 hours. The alternates keep their incoming order rather than
// being sorted, because orderReaderCandidates keeps that order between sources
// of equal rank, so it is part of the question too.
func chapterCacheKey(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef) string {
	var key strings.Builder
	key.WriteString(strings.ToLower(strings.TrimSpace(sourceKey)))
	key.WriteString("|")
	key.WriteString(strings.ToLower(strings.TrimSpace(sourceURL)))
	key.WriteString("|")
	key.WriteString(strconv.FormatFloat(chapter, 'f', -1, 64))
	for _, alternate := range alternates {
		alternateKey := strings.TrimSpace(alternate.SourceKey)
		alternateURL := strings.TrimSpace(alternate.SourceURL)
		// fetch skips these as well, so they cannot change the answer.
		if alternateKey == "" || alternateURL == "" {
			continue
		}
		key.WriteString("|")
		key.WriteString(strings.ToLower(alternateKey))
		key.WriteString("=")
		key.WriteString(strings.ToLower(alternateURL))
	}
	return key.String()
}

// preferringCacheKey is chapterCacheKey for a lookup whose chain is headed by a
// preferred site (readingChain's effective preference; empty for the ranked
// chain, which keeps the plain key). The preference goes last so the key still
// starts with the link's scope, which is what InvalidateSource matches on, and
// is written with a colon where an alternate has "=", so it cannot be read as
// one.
func preferringCacheKey(preferred, sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef) string {
	key := chapterCacheKey(sourceKey, sourceURL, chapter, alternates)
	if preferred == "" {
		return key
	}
	return key + "|prefer:" + preferred
}

func (r *ChapterLinkResolver) cached(cacheKey string) (chapterURL string, found bool, ok bool) {
	entry, exists := r.cache.get(cacheKey)
	if !exists {
		return "", false, false
	}

	if now := time.Now().UTC(); entry.expired(now) {
		// Conditional: a fetch may have refreshed the key since the read above.
		r.cache.dropIf(cacheKey, func(current chapterEntry) bool { return current.expired(now) })
		return "", false, false
	}

	return entry.ChapterURL, entry.Found, true
}

// cacheResult writes a resolve's answer unless its link (scope) was
// invalidated since the resolve was queued at generation.
func (r *ChapterLinkResolver) cacheResult(generation uint64, scope, cacheKey, chapterURL string, found bool, ttl time.Duration) {
	r.cache.putIfGeneration(generation, scope, cacheKey, chapterEntry{
		ChapterURL: chapterURL,
		Found:      found,
		ExpiresAt:  time.Now().UTC().Add(ttl),
	})
}
