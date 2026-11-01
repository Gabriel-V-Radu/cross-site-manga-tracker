package resolve

import (
	"context"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// TestFetchChapterURLFallsBackToAlternate covers the sibling defect: chapter links
// pointing at a source that cannot resolve them.
func TestFetchChapterURLFallsBackToAlternate(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "mirrorsource"}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedsource", "https://blocked.example/title/a", 440.2, alternates)
	if err != nil {
		t.Fatalf("expected the alternate to resolve the chapter: %v", err)
	}
	if want := "https://mirror.example/series/a.ZD1/chapter-440.2"; chapterURL != want {
		t.Fatalf("expected %q, got %q", want, chapterURL)
	}
}

// TestFetchChapterURLPrefersPrimary makes sure the fallback only engages on
// failure and a healthy primary is still used.
func TestFetchChapterURLPrefersPrimary(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "primarysource"}); err != nil {
		t.Fatalf("register primary connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "mirrorsource"}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "primarysource", "https://primary.example/title/a", 12, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if want := "https://primary.example/title/a/chapter-12"; chapterURL != want {
		t.Fatalf("expected the primary source to win, got %q", chapterURL)
	}
}

// TestFetchChapterURLNegativeCacheIsShortAfterAnAttempt distinguishes a live
// failure, which may clear on its own, from a structurally unusable source.
func TestFetchChapterURLNegativeCacheIsShortAfterAnAttempt(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)

	if _, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedsource", "https://blocked.example/title/a", 5, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}

	entry, exists := resolver.cache.get(chapterCacheKey("blockedsource", "https://blocked.example/title/a", 5, nil))
	if !exists {
		t.Fatalf("expected a negative cache entry")
	}
	if remaining := time.Until(entry.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the retry negative TTL after a real attempt, got %s", remaining)
	}
}

// TestFetchChapterURLUnknownConnectorCachesLonger pins the other half of that
// distinction: nothing was queried, so nothing will change soon.
func TestFetchChapterURLUnknownConnectorCachesLonger(t *testing.T) {
	resolver := newChapterTestResolver(t, connectors.NewRegistry())

	if _, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "nosuchsource", "https://nowhere.example/title/a", 5, nil); err == nil {
		t.Fatalf("expected an error for an unregistered connector")
	}

	entry, exists := resolver.cache.get(chapterCacheKey("nosuchsource", "https://nowhere.example/title/a", 5, nil))
	if !exists {
		t.Fatalf("expected a negative cache entry")
	}
	// Strictly longer than anything the retry span can produce, jitter included:
	// that gap is the whole point of the distinction.
	if remaining := time.Until(entry.ExpiresAt); remaining <= maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected a longer negative TTL when nothing was queried, got %s", remaining)
	}
}

// TestFetchChapterURLFallsBackToOfflineGuess covers the reader whose fresh
// chapter number came from a tracking-only source while every readable site
// is unresolvable server-side: the constructed reader link is served rather
// than nothing, because the reader's own browser can pass the challenge.
func TestFetchChapterURLFallsBackToOfflineGuess(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "blockedsource"}}); err != nil {
		t.Fatalf("register blocked linkable connector: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedsource", "https://blocked.example/title/a", 177, nil)
	if err != nil {
		t.Fatalf("expected the offline guess to be served: %v", err)
	}
	if chapterURL != "https://blocked.example/title/a/read/chapter-177" {
		t.Fatalf("unexpected guessed url %q", chapterURL)
	}
}

// TestFetchChapterURLVerifiedReaderBeatsBlockedGuess pins the tier order: a
// readable site that verified it carries the chapter beats a blocked site's
// unverified built link, even when the blocked site is the tracker's primary.
// (The built link used to win its row-order turn, which badged a
// challenge-blocked MangaFire — pointing at a chapter nobody confirmed —
// while a live site carried the chapter one hop away.)
func TestFetchChapterURLVerifiedReaderBeatsBlockedGuess(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "blockedsource"}}); err != nil {
		t.Fatalf("register blocked linkable connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "mirrorsource"}); err != nil {
		t.Fatalf("register mirror connector: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedsource", "https://blocked.example/title/a", 12, alternates)
	if err != nil {
		t.Fatalf("expected the verified mirror to be served: %v", err)
	}
	if chapterURL != "https://mirror.example/series/a.ZD1/chapter-12" {
		t.Fatalf("expected the verified mirror to beat the blocked primary's guess, got %q", chapterURL)
	}
}

// TestFetchChapterURLBlockedGuessBeatsInfoFloor pins the other side of that
// tier: with every readable site unable to verify, the blocked site's built
// link still beats the info floor — the reader's own browser passes the
// challenge the server cannot, and nobody wants to read on ComicK.
func TestFetchChapterURLBlockedGuessBeatsInfoFloor(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "blockedsource"}}); err != nil {
		t.Fatalf("register blocked linkable connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "comick", siteTier: siteTier{rank: connectors.ReaderRankInfoFloor}}); err != nil {
		t.Fatalf("register comick stub: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
	}

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedsource", "https://blocked.example/title/a", 12, alternates)
	if err != nil {
		t.Fatalf("expected the built url to be served: %v", err)
	}
	if chapterURL != "https://blocked.example/title/a/read/chapter-12" {
		t.Fatalf("expected the blocked site's built url to beat the info floor, got %q", chapterURL)
	}
}

// TestFetchChapterURLAnsweredNotFoundNeverBuildsTheGuess pins the bug that
// badged MangaFire on cards whose chapter it did not carry: a site whose
// resolver *answered* "not carried" must cede its turn entirely — its
// offline-built link is a page known not to exist — leaving the chapter to
// the next site in the chain.
func TestFetchChapterURLAnsweredNotFoundNeverBuildsTheGuess(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(linkableCappedConnector{cappedResolverConnector{mirrorConnector{key: "answeredsource"}, 81}}); err != nil {
		t.Fatalf("register answered connector: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "comick", siteTier: siteTier{rank: connectors.ReaderRankInfoFloor}}); err != nil {
		t.Fatalf("register comick stub: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
	}

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "answeredsource", "https://answered.example/title/a", 82, alternates)
	if err != nil {
		t.Fatalf("expected the floor to serve the chapter: %v", err)
	}
	if chapterURL != "https://comick.example/comic/a/chapter-82" {
		t.Fatalf("expected the answered refusal to fall through past the guess, got %q", chapterURL)
	}
}

// TestFetchChapterURLOriginSiteOutranksAggregators pins the top of the chain:
// an origin scanlator site that verified it carries the chapter wins over a
// fresh MangaHub, a blocked-but-linkable default-tier site, and the ComicK
// floor all at once.
func TestFetchChapterURLOriginSiteOutranksAggregators(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "asuracomic", siteTier: siteTier{rank: connectors.ReaderRankOrigin}}); err != nil {
		t.Fatalf("register asuracomic stub: %v", err)
	}
	if err := registry.Register(cappedResolverConnector{mirrorConnector{key: "mangahub", siteTier: siteTier{rank: connectors.ReaderRankFreshAggregator}}, 100}); err != nil {
		t.Fatalf("register mangahub stub: %v", err)
	}
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "blockedreader", siteTier: siteTier{rank: connectors.ReaderRankDefault}}}); err != nil {
		t.Fatalf("register blocked linkable stub: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "comick", siteTier: siteTier{rank: connectors.ReaderRankInfoFloor}}); err != nil {
		t.Fatalf("register comick stub: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)
	// Asura deliberately last in row order: its precedence must come from the
	// ranking, not from where the link scan happened to insert it.
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
		{SourceKey: "mangahub", SourceURL: "https://mangahub.example/manga/a"},
		{SourceKey: "asuracomic", SourceURL: "https://asura.example/comics/a"},
	}

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedreader", "https://blocked.example/title/a", 82, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://asura.example/comics/a/chapter-82" {
		t.Fatalf("expected the origin site to win the chain, got %q", chapterURL)
	}
}

// newReaderPriorityResolver wires the three sources of the reading chain the
// way a tracker whose primary is a challenge-blocked, offline-linkable
// default-tier site carries them: that site primary, ComicK and MangaHub
// alternates — deliberately in that linked order, since ComicK was linked first
// in production and the priority must come from ranking, not row order.
func newReaderPriorityResolver(t *testing.T, mangahubLatest float64) (*ChapterLinkResolver, []repository.TrackerSourceRef) {
	t.Helper()
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "blockedreader", siteTier: siteTier{rank: connectors.ReaderRankDefault}}}); err != nil {
		t.Fatalf("register blocked linkable stub: %v", err)
	}
	if err := registry.Register(mirrorConnector{key: "comick", siteTier: siteTier{rank: connectors.ReaderRankInfoFloor}}); err != nil {
		t.Fatalf("register comick stub: %v", err)
	}
	if err := registry.Register(cappedResolverConnector{mirrorConnector{key: "mangahub", siteTier: siteTier{rank: connectors.ReaderRankFreshAggregator}}, mangahubLatest}); err != nil {
		t.Fatalf("register mangahub stub: %v", err)
	}

	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
		{SourceKey: "mangahub", SourceURL: "https://mangahub.example/manga/a"},
	}
	return newChapterTestResolver(t, registry), alternates
}

// TestFetchChapterURLPrefersFreshMangaHub pins step 1 of the reading chain:
// when MangaHub carries the requested chapter, reading happens there, even
// though the blocked primary could build a link and ComicK could resolve.
func TestFetchChapterURLPrefersFreshMangaHub(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedreader", "https://blocked.example/title/a", 99, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://mangahub.example/manga/a/chapter-99" {
		t.Fatalf("expected mangahub to win when fresh, got %q", chapterURL)
	}
}

// TestFetchChapterURLStaleMangaHubFallsBackToBlockedGuess pins step 2: a chapter
// MangaHub does not carry yet must open on the blocked primary's built link, not
// on MangaHub's missing page and not on ComicK.
func TestFetchChapterURLStaleMangaHubFallsBackToBlockedGuess(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "blockedreader", "https://blocked.example/title/a", 101, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://blocked.example/title/a/read/chapter-101" {
		t.Fatalf("expected the blocked primary's built link when mangahub is stale, got %q", chapterURL)
	}
}

// TestFetchChapterURLWithoutBlockedGuessFallsBackToComicK pins step 3: with no
// buildable link and MangaHub stale, ComicK is the floor. This is the shape
// every MangaFire-primary tracker takes now that MangaFire builds no offline
// links.
func TestFetchChapterURLWithoutBlockedGuessFallsBackToComicK(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	// The tracker's primary is ComicK here — nothing buildable in the chain.
	chapterURL, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "comick", "https://comick.example/comic/a", 101, alternates[1:])
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://comick.example/comic/a/chapter-101" {
		t.Fatalf("expected comick as the floor, got %q", chapterURL)
	}
}

// TestFetchChapterURLFloorAnswerIsProvisional pins how long the floor is
// allowed to keep a card. A chapter minutes old is on ComicK before anywhere
// else, so the chain legitimately lands on the floor — but MangaHub and
// MangaFire index it within the hour, and remembering the floor's answer for
// the verified span would leave the card opening the worst site in the chain
// all day. (That is exactly what happened to a MangaFire-primary tracker whose
// chapter 115 kept opening ComicK hours after both better sites carried it.)
func TestFetchChapterURLFloorAnswerIsProvisional(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	// ComicK primary, MangaHub the only alternate and one chapter behind.
	const sourceURL = "https://comick.example/comic/a"
	if _, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "comick", sourceURL, 101, alternates[1:]); err != nil {
		t.Fatalf("expected the floor to serve the chapter: %v", err)
	}

	entry, exists := resolver.cache.get(chapterCacheKey("comick", sourceURL, 101, alternates[1:]))
	if !exists {
		t.Fatalf("expected the floor answer to be cached")
	}
	if remaining := time.Until(entry.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the floor answer held for the retry span, got %s", remaining)
	}
}

// TestFetchChapterURLFloorOnlyChainKeepsTheVerifiedSpan is the other half: with
// no better-ranked site in the chain, the floor's answer is not provisional —
// nothing is coming to replace it — so re-resolving it every retry span would
// be a request per render for an answer that cannot change.
func TestFetchChapterURLFloorOnlyChainKeepsTheVerifiedSpan(t *testing.T) {
	resolver, _ := newReaderPriorityResolver(t, 100)

	const sourceURL = "https://comick.example/comic/a"
	if _, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "comick", sourceURL, 101, nil); err != nil {
		t.Fatalf("expected the floor to serve the chapter: %v", err)
	}

	entry, exists := resolver.cache.get(chapterCacheKey("comick", sourceURL, 101, nil))
	if !exists {
		t.Fatalf("expected the floor answer to be cached")
	}
	if remaining := time.Until(entry.ExpiresAt); remaining <= maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the verified span when nothing better was asked, got %s", remaining)
	}
}

// TestOrderReaderCandidates pins the ranking itself: origin scanlator sites
// first, then MangaHub, ComicK last, everything else keeps its incoming order.
// It runs against the connector set the app actually ships, since that is where
// the tiers are published — a connector that changes its rank sends readers to
// a worse site, and this is where that shows up.
func TestOrderReaderCandidates(t *testing.T) {
	resolver := newChapterTestResolver(t, connectordefaults.NewRegistry())
	candidates := []repository.TrackerSourceRef{
		{SourceKey: "mangafire"},
		{SourceKey: "mangaupdates"},
		{SourceKey: "comick"},
		{SourceKey: "asuracomic"},
		{SourceKey: "mangadex"},
		{SourceKey: "mangahub"},
		{SourceKey: "flamecomics"},
	}
	orderReaderCandidates(candidates, resolver.readerCandidateRank)

	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.SourceKey)
	}
	want := []string{"asuracomic", "flamecomics", "mangahub", "mangafire", "mangaupdates", "mangadex", "comick"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unexpected order %v, want %v", got, want)
		}
	}
}

// TestLookupQueuesTheResolve pins the queued path a first render takes: the
// series page is served immediately and reported unresolved, and the real link
// lands in the cache once the background resolve finishes.
func TestLookupQueuesTheResolve(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "mangafire"}); err != nil {
		t.Fatalf("register mangafire connector: %v", err)
	}

	resolver := newChapterTestResolver(t, registry)

	sourceURL := "https://mangafire.to/manga/one-piecee.dkw"
	chapter := 1173.0

	resolvedURL, resolved, waiting := resolver.Lookup("mangafire", sourceURL, chapter, nil, "")
	if resolvedURL != sourceURL {
		t.Fatalf("expected initial URL %q, got %q", sourceURL, resolvedURL)
	}
	if resolved {
		t.Fatalf("expected the queued URL to be reported as unresolved")
	}
	if !waiting {
		t.Fatalf("expected mangafire chapter url resolution to be queued")
	}

	cacheKey := chapterCacheKey("mangafire", sourceURL, chapter, nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if chapterURL, found, ok := resolver.cached(cacheKey); ok && found {
			expected := sourceURL + "/chapter-1173"
			if chapterURL != expected {
				t.Fatalf("expected cached chapter URL %q, got %q", expected, chapterURL)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected chapter URL cache entry for mangafire")
}

// TestChapterCacheKeyCoversTheCandidateSet pins what makes two chapter-link
// questions the same one: the primary, the chapter, and the alternates in
// their incoming order. Anything the chain could answer differently on must
// change the key; spelling the same links differently must not.
func TestChapterCacheKeyCoversTheCandidateSet(t *testing.T) {
	const primaryKey, primaryURL = "comick", "https://comick.example/comic/a"
	mangahub := repository.TrackerSourceRef{SourceKey: "mangahub", SourceURL: "https://mangahub.example/manga/a"}
	asura := repository.TrackerSourceRef{SourceKey: "asuracomic", SourceURL: "https://asura.example/comics/a"}
	autoChain := chapterCacheKey(primaryKey, primaryURL, 12, []repository.TrackerSourceRef{mangahub, asura})

	tests := []struct {
		name       string
		alternates []repository.TrackerSourceRef
		same       bool
	}{
		{"a pin to the primary passes no alternates", nil, false},
		{"a newly linked source", []repository.TrackerSourceRef{mangahub, asura, {SourceKey: "mangadex", SourceURL: "https://mangadex.example/title/a"}}, false},
		{"an unlinked source", []repository.TrackerSourceRef{mangahub}, false},
		{"a relinked url on the same site", []repository.TrackerSourceRef{mangahub, {SourceKey: "asuracomic", SourceURL: "https://asura.example/comics/b"}}, false},
		// Not sorted: between sources of equal rank the incoming order picks.
		{"the same links in another order", []repository.TrackerSourceRef{asura, mangahub}, false},
		{"the same links spelled with other case and spacing", []repository.TrackerSourceRef{{SourceKey: " MangaHub ", SourceURL: "https://MangaHub.example/manga/a "}, asura}, true},
		{"a blank link the chain skips anyway", []repository.TrackerSourceRef{mangahub, {SourceKey: "mangadex"}, asura}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := chapterCacheKey(primaryKey, primaryURL, 12, test.alternates)
			if (got == autoChain) != test.same {
				t.Fatalf("key %q against the auto chain's %q: same=%v, want %v", got, autoChain, got == autoChain, test.same)
			}
		})
	}
}

// TestFetchChapterURLPinAndAutoChainAnswerSeparately pins the leak between two
// questions a card can ask of one primary. Keyed on the primary alone,
// whichever was asked first answered both: a pin to ComicK (which passes no
// alternates) cached ComicK's link for the floor's 12 hours, and the auto chain
// with MangaHub linked — the same card unpinned, or another profile tracking
// the same URL — then opened ComicK as well. Asked the other way round, the
// pinned card opened MangaHub, breaking the pin's "never another site".
func TestFetchChapterURLPinAndAutoChainAnswerSeparately(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)
	const sourceURL = "https://comick.example/comic/a"
	withMangaHub := alternates[1:]

	pinned, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "comick", sourceURL, 99, nil)
	if err != nil {
		t.Fatalf("pinned resolve failed: %v", err)
	}
	if pinned != sourceURL+"/chapter-99" {
		t.Fatalf("expected the pin to open ComicK, got %q", pinned)
	}

	auto, err := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "comick", sourceURL, 99, withMangaHub)
	if err != nil {
		t.Fatalf("auto resolve failed: %v", err)
	}
	if auto != "https://mangahub.example/manga/a/chapter-99" {
		t.Fatalf("expected the auto chain to open MangaHub despite the pinned answer cached first, got %q", auto)
	}

	// Each answer stays cached under its own question.
	if again, _ := resolver.fetch(context.Background(), resolver.cache.currentGeneration(), "comick", sourceURL, 99, nil); again != pinned {
		t.Fatalf("expected the pin to keep opening ComicK, got %q", again)
	}
}

// TestInvalidateVoidsAChapterResolveAlreadyInFlight pins the race Invalidate
// used to lose: it reset the map, the resolve already in flight — computed from
// the links as they were — wrote its answer straight after, and the queue
// refused the fresh resolve the re-rendered card asked for while that one was
// still running. The stale answer then held for up to 12 hours.
func TestInvalidateVoidsAChapterResolveAlreadyInFlight(t *testing.T) {
	registry := connectors.NewRegistry()
	connector := newGatedConnector("gatedsource", false)
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register gated connector: %v", err)
	}
	resolver := newChapterTestResolver(t, registry)

	const sourceURL = "https://gated.example/title/a"
	cacheKey := chapterCacheKey("gatedsource", sourceURL, 12, nil)
	if _, _, waiting := resolver.Lookup("gatedsource", sourceURL, 12, nil, ""); !waiting {
		t.Fatal("expected a background resolve to be queued")
	}
	waitGateStarted(t, connector)

	resolver.Invalidate()
	close(connector.release)
	waitQueueIdle(t, resolver.queue)

	if entry, exists := resolver.cache.get(cacheKey); exists {
		t.Fatalf("a resolve in flight across Invalidate wrote its answer after it: %+v", entry)
	}

	// The next render asks again, and that answer lands.
	if _, _, waiting := resolver.Lookup("gatedsource", sourceURL, 12, nil, ""); !waiting {
		t.Fatal("expected the next render to queue a fresh resolve")
	}
	waitQueueIdle(t, resolver.queue)
	if chapterURL, found, ok := resolver.cached(cacheKey); !ok || !found || chapterURL != sourceURL+"/chapter-12" {
		t.Fatalf("expected the fresh resolve's answer cached, got %q (found=%v ok=%v)", chapterURL, found, ok)
	}
}

// TestInvalidateVoidsAChapterResolveStillQueued covers a resolve that was only
// waiting for a slot when Invalidate ran. Its generation is the one captured
// when it was queued, so it writes nothing — and since nothing could be
// written, it does not spend a request on the site either.
func TestInvalidateVoidsAChapterResolveStillQueued(t *testing.T) {
	registry := connectors.NewRegistry()
	connector := newGatedConnector("gatedsource", false)
	close(connector.release)
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register gated connector: %v", err)
	}
	resolver := newChapterTestResolver(t, registry)

	// Every slot is taken, so the resolve queues behind them.
	for i := 0; i < cap(resolver.sem); i++ {
		resolver.sem <- struct{}{}
	}
	const sourceURL = "https://gated.example/title/a"
	if _, _, waiting := resolver.Lookup("gatedsource", sourceURL, 12, nil, ""); !waiting {
		t.Fatal("expected a background resolve to be queued")
	}

	resolver.Invalidate()
	<-resolver.sem // one slot frees up after the invalidation
	waitQueueIdle(t, resolver.queue)

	select {
	case <-connector.started:
		t.Fatal("a resolve voided while queued still asked the site")
	default:
	}
	if entry, exists := resolver.cache.get(chapterCacheKey("gatedsource", sourceURL, 12, nil)); exists {
		t.Fatalf("a resolve queued across Invalidate wrote its answer after it: %+v", entry)
	}
}
