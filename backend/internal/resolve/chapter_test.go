package resolve

import (
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

	chapterURL, err := resolver.fetch("blockedsource", "https://blocked.example/title/a", 440.2, alternates)
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

	chapterURL, err := resolver.fetch("primarysource", "https://primary.example/title/a", 12, alternates)
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

	if _, err := resolver.fetch("blockedsource", "https://blocked.example/title/a", 5, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}

	entry, exists := resolver.cache.get(chapterCacheKey("blockedsource", "https://blocked.example/title/a", 5))
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

	if _, err := resolver.fetch("nosuchsource", "https://nowhere.example/title/a", 5, nil); err == nil {
		t.Fatalf("expected an error for an unregistered connector")
	}

	entry, exists := resolver.cache.get(chapterCacheKey("nosuchsource", "https://nowhere.example/title/a", 5))
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

	chapterURL, err := resolver.fetch("blockedsource", "https://blocked.example/title/a", 177, nil)
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

	chapterURL, err := resolver.fetch("blockedsource", "https://blocked.example/title/a", 12, alternates)
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

	chapterURL, err := resolver.fetch("blockedsource", "https://blocked.example/title/a", 12, alternates)
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

	chapterURL, err := resolver.fetch("answeredsource", "https://answered.example/title/a", 82, alternates)
	if err != nil {
		t.Fatalf("expected the floor to serve the chapter: %v", err)
	}
	if chapterURL != "https://comick.example/comic/a/chapter-82" {
		t.Fatalf("expected the answered refusal to fall through past the guess, got %q", chapterURL)
	}
}

// TestFetchChapterURLOriginSiteOutranksAggregators pins the top of the chain:
// an origin scanlator site that verified it carries the chapter wins over a
// fresh MangaHub, a linkable MangaFire, and the ComicK floor all at once.
func TestFetchChapterURLOriginSiteOutranksAggregators(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(mirrorConnector{key: "asuracomic", siteTier: siteTier{rank: connectors.ReaderRankOrigin}}); err != nil {
		t.Fatalf("register asuracomic stub: %v", err)
	}
	if err := registry.Register(cappedResolverConnector{mirrorConnector{key: "mangahub", siteTier: siteTier{rank: connectors.ReaderRankFreshAggregator}}, 100}); err != nil {
		t.Fatalf("register mangahub stub: %v", err)
	}
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "mangafire", siteTier: siteTier{rank: connectors.ReaderRankDefault}}}); err != nil {
		t.Fatalf("register mangafire stub: %v", err)
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

	chapterURL, err := resolver.fetch("mangafire", "https://mangafire.example/title/a", 82, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://asura.example/comics/a/chapter-82" {
		t.Fatalf("expected the origin site to win the chain, got %q", chapterURL)
	}
}

// newReaderPriorityResolver wires the three sources of the reading chain the
// way a MangaFire-primary tracker carries them: MangaFire primary
// (challenge-blocked, offline-linkable), ComicK and MangaHub alternates —
// deliberately in that linked order, since ComicK was linked first in
// production and the priority must come from ranking, not row order.
func newReaderPriorityResolver(t *testing.T, mangahubLatest float64) (*ChapterLinkResolver, []repository.TrackerSourceRef) {
	t.Helper()
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedLinkableConnector{blockedConnector{key: "mangafire", siteTier: siteTier{rank: connectors.ReaderRankDefault}}}); err != nil {
		t.Fatalf("register mangafire stub: %v", err)
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
// though the MangaFire primary could build a link and ComicK could resolve.
func TestFetchChapterURLPrefersFreshMangaHub(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	chapterURL, err := resolver.fetch("mangafire", "https://mangafire.example/title/a", 99, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://mangahub.example/manga/a/chapter-99" {
		t.Fatalf("expected mangahub to win when fresh, got %q", chapterURL)
	}
}

// TestFetchChapterURLStaleMangaHubFallsBackToMangaFire pins step 2: a chapter
// MangaHub does not carry yet must open on MangaFire's built link, not on
// MangaHub's missing page and not on ComicK.
func TestFetchChapterURLStaleMangaHubFallsBackToMangaFire(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	chapterURL, err := resolver.fetch("mangafire", "https://mangafire.example/title/a", 101, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://mangafire.example/title/a/read/chapter-101" {
		t.Fatalf("expected mangafire's built link when mangahub is stale, got %q", chapterURL)
	}
}

// TestFetchChapterURLWithoutMangaFireFallsBackToComicK pins step 3: with no
// MangaFire link and MangaHub stale, ComicK is the floor.
func TestFetchChapterURLWithoutMangaFireFallsBackToComicK(t *testing.T) {
	resolver, alternates := newReaderPriorityResolver(t, 100)

	// The tracker's primary is ComicK here — no MangaFire in the chain.
	chapterURL, err := resolver.fetch("comick", "https://comick.example/comic/a", 101, alternates[1:])
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://comick.example/comic/a/chapter-101" {
		t.Fatalf("expected comick as the floor, got %q", chapterURL)
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

	cacheKey := chapterCacheKey("mangafire", sourceURL, chapter)
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
