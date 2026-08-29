package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// siteTier is the SiteInfo a double publishes. The reading chain reads each
// site's tier off its connector, so a double standing in for a ranked site
// (the fresh aggregator, the info floor) has to say which one it is, exactly
// as the real connectors do. Left unset it is the origin tier, which is what a
// double standing in for a tracker's own primary wants. The hosts stay empty:
// these doubles are reached by key, and claiming a host would enter them into
// the registry's URL routing for no reason.
type siteTier struct{ rank int }

func (s siteTier) Hosts() []string { return nil }
func (s siteTier) HomeURL() string { return "" }
func (s siteTier) ReaderRank() int { return s.rank }

// blockedConnector stands in for a source behind a bot challenge: it is
// registered and reachable in principle, but every resolve fails.
type blockedConnector struct {
	siteTier
	key string
}

func (b blockedConnector) Key() string                       { return b.key }
func (b blockedConnector) Name() string                      { return b.key }
func (b blockedConnector) Kind() string                      { return connectors.KindNative }
func (b blockedConnector) HealthCheck(context.Context) error { return errors.New("blocked") }
func (b blockedConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (b blockedConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, errors.New("behind a browser challenge")
}
func (b blockedConnector) ResolveChapterURL(context.Context, string, float64) (string, error) {
	return "", errors.New("behind a browser challenge")
}

// mirrorConnector stands in for a working alternate source.
type mirrorConnector struct {
	siteTier
	key   string
	cover string
}

func (m mirrorConnector) Key() string                       { return m.key }
func (m mirrorConnector) Name() string                      { return m.key }
func (m mirrorConnector) Kind() string                      { return connectors.KindNative }
func (m mirrorConnector) HealthCheck(context.Context) error { return nil }
func (m mirrorConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (m mirrorConnector) ResolveByURL(_ context.Context, rawURL string) (*connectors.MangaResult, error) {
	return &connectors.MangaResult{SourceKey: m.key, URL: rawURL, CoverImageURL: m.cover}, nil
}
func (m mirrorConnector) ResolveChapterURL(_ context.Context, rawURL string, chapter float64) (string, error) {
	return rawURL + "/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), nil
}

func newFallbackHandler(t *testing.T, registry *connectors.Registry) *DashboardHandler {
	t.Helper()
	return &DashboardHandler{
		registry:           registry,
		coverCache:         make(map[string]coverCacheEntry),
		coverInFlight:      make(map[string]bool),
		coverFetchSem:      make(chan struct{}, 2),
		mangafireCoverSem:  make(chan struct{}, 1),
		chapterURLCache:    make(map[string]chapterURLCacheEntry),
		chapterURLInFlight: make(map[string]bool),
		chapterURLFetchSem: make(chan struct{}, 2),
		// Tests must not probe real image hosts.
		coverURLChecker: func(context.Context, string) bool { return true },
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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	coverURL, err := h.fetchCoverURL(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, alternates)
	if err != nil {
		t.Fatalf("expected the alternate to supply a cover: %v", err)
	}
	if coverURL != "https://cdn.example/cover.webp" {
		t.Fatalf("unexpected cover url %q", coverURL)
	}

	// The result must be cached under the primary key, so the next render of the
	// same tracker is served without re-querying either site.
	cacheKey := buildCoverCacheKey("blockedsource", "https://blocked.example/title/a", nil)
	cached, found, ok := h.getCachedCover(cacheKey)
	if !ok || !found {
		t.Fatalf("expected the fallback cover to be cached under the primary key")
	}
	if cached != coverURL {
		t.Fatalf("cached cover %q does not match resolved %q", cached, coverURL)
	}
}

// TestFindServingSource pins which source a card presents. A card must never
// badge a site that supplied nothing while its links point somewhere else.
func TestFindServingSource(t *testing.T) {
	alternates := []repository.TrackerSourceRef{
		{SourceID: 9, SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
		{SourceID: 4, SourceKey: "nourlsource", SourceURL: "   "},
		{SourceID: 0, SourceKey: "noidsource", SourceURL: "https://noid.example/x"},
	}

	cases := []struct {
		name        string
		servingKey  string
		primaryKey  string
		wantFound   bool
		wantURL     string
		wantSourceI int64
	}{
		{
			name:        "fallback served the card",
			servingKey:  "mirrorsource",
			primaryKey:  "blockedsource",
			wantFound:   true,
			wantURL:     "https://mirror.example/series/a.ZD1",
			wantSourceI: 9,
		},
		{name: "primary served the card", servingKey: "blockedsource", primaryKey: "blockedsource"},
		{name: "primary match ignores case", servingKey: "BlockedSource", primaryKey: "blockedsource"},
		{name: "nothing resolved yet", servingKey: "", primaryKey: "blockedsource"},
		{name: "serving key is not a linked alternate", servingKey: "elsewhere", primaryKey: "blockedsource"},
		{name: "alternate without a url is unusable", servingKey: "nourlsource", primaryKey: "blockedsource"},
		{name: "alternate without a source id is unusable", servingKey: "noidsource", primaryKey: "blockedsource"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := findServingSource(testCase.servingKey, testCase.primaryKey, alternates)
			if ok != testCase.wantFound {
				t.Fatalf("expected found=%v, got %v", testCase.wantFound, ok)
			}
			if !testCase.wantFound {
				return
			}
			if got.SourceURL != testCase.wantURL {
				t.Fatalf("expected url %q, got %q", testCase.wantURL, got.SourceURL)
			}
			if got.SourceID != testCase.wantSourceI {
				t.Fatalf("expected source id %d, got %d", testCase.wantSourceI, got.SourceID)
			}
		})
	}
}

// mixedSourceCard builds the card for a tracker whose cover came from a mirror,
// seeding every cache so buildTrackerCards resolves without touching a connector.
// This is the shape the live dashboard produced while MangaFire was flapping.
func mixedSourceCard(t *testing.T, latestChapterURL string, latestResolved bool) trackerCardView {
	t.Helper()

	const (
		primaryURL = "https://mangafire.to/title/npozj-akanabe"
		mirrorURL  = "https://comick.dev/comic/akanabe"
	)
	latest, lastRead := 65.0, 59.0

	// The real connector set, for its host claims alone: the URLs below are the
	// production ones, and attributing them to a site is what the badge is being
	// checked on here. Every lookup is seeded into the caches, so nothing is
	// resolved through it.
	h := newFallbackHandler(t, connectordefaults.NewRegistry())

	// The cover came from the mirror: the primary's art endpoint was unreachable.
	h.setCachedCoverFromSource(buildCoverCacheKey("mangafire", primaryURL, nil),
		"https://cdn.example/cover.webp", "comick", true, time.Hour)

	h.setCachedChapterURL(buildChapterURLCacheKey("mangafire", primaryURL, latest),
		latestChapterURL, latestResolved, time.Hour)
	// The older chapter sits deep in the primary's paged listing and only the
	// mirror could answer for it.
	h.setCachedChapterURL(buildChapterURLCacheKey("mangafire", primaryURL, lastRead),
		mirrorURL+"/chapter-59", true, time.Hour)

	items := []models.Tracker{{
		ID: 1, Title: "Akanabe", Status: "reading", SourceID: 1,
		SourceURL:          primaryURL,
		LatestKnownChapter: &latest,
		LastReadChapter:    &lastRead,
	}}
	sourceByID := map[int64]models.Source{
		1: {ID: 1, Key: "mangafire", Name: "MangaFire"},
		2: {ID: 2, Key: "comick", Name: "ComicK"},
	}
	logos := map[int64]string{1: "/logos/mangafire.png", 2: "/logos/comick.png"}
	alternates := map[int64][]repository.TrackerSourceRef{
		1: {{SourceID: 2, SourceKey: "comick", SourceURL: mirrorURL}},
	}

	cards, pending := h.buildTrackerCards(items, sourceByID, logos, alternates, "")
	if pending {
		t.Fatalf("expected every lookup to be served from cache")
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	return cards[0]
}

// TestBuildTrackerCardsPresentsTheChapterSourceNotTheCoverSource pins the defect
// behind "some buttons open MangaFire, others the mirror": the badge and its Open
// link were driven by whoever supplied the cover, which is the weakest signal on
// the card. Cover art and chapters resolve through different endpoints, so a card
// could badge the mirror while its newest-chapter link opened the primary.
func TestBuildTrackerCardsPresentsTheChapterSourceNotTheCoverSource(t *testing.T) {
	card := mixedSourceCard(t, "https://mangafire.to/title/npozj-akanabe/9353930", true)

	if card.SourceLogoLabel != "MangaFire" {
		t.Fatalf("expected the card to present the site holding the newest chapter, got %q", card.SourceLogoLabel)
	}
	if card.SourceLogoURL != "/logos/mangafire.png" {
		t.Fatalf("expected the primary's logo, got %q", card.SourceLogoURL)
	}
	if card.SourceURL != "https://mangafire.to/title/npozj-akanabe" {
		t.Fatalf("expected Open to follow the newest chapter's site, got %q", card.SourceURL)
	}

	// Each link still goes wherever that chapter actually exists — forcing both
	// onto one site would break whichever one that site cannot serve — so each
	// says where it lands.
	if card.LatestKnownChapterSite != "MangaFire" {
		t.Fatalf("expected the latest chapter to be labelled MangaFire, got %q", card.LatestKnownChapterSite)
	}
	if card.LastReadChapterSite != "ComicK" {
		t.Fatalf("expected the last read chapter to be labelled ComicK, got %q", card.LastReadChapterSite)
	}
}

// TestBuildTrackerCardsFallsBackToTheCoverSource is the other half: with no
// resolved chapter link and no recorded reporter there is nothing better to go
// on, so the cover's source still decides. Without this the card would badge a
// primary that served nothing.
func TestBuildTrackerCardsFallsBackToTheCoverSource(t *testing.T) {
	// A negative cache entry: the link degrades to the series page, which names
	// the primary but proves nothing about who can serve the chapter.
	card := mixedSourceCard(t, "", false)

	if card.SourceLogoLabel != "ComicK" {
		t.Fatalf("expected the cover's source to decide when no chapter link resolved, got %q", card.SourceLogoLabel)
	}
	if card.SourceURL != "https://comick.dev/comic/akanabe" {
		t.Fatalf("expected Open to follow the mirror, got %q", card.SourceURL)
	}
	if card.LatestKnownChapterSite != "" {
		t.Fatalf("expected no label for an unresolved link, got %q", card.LatestKnownChapterSite)
	}
}

// TestBuildTrackerCardsPresentsTheChapterReporter sits between those two: no
// chapter link resolved, but the poller recorded which source reported the
// stored number. That source outranks the cover's — the number on the card is
// its claim, so the badge and the degraded latest-chapter link follow it
// rather than a site that merely supplied art.
func TestBuildTrackerCardsPresentsTheChapterReporter(t *testing.T) {
	const (
		primaryURL  = "https://mangafire.to/title/npozj-akanabe"
		reporterURL = "https://comick.dev/comic/akanabe"
		artURL      = "https://mangahub.example/manga/akanabe"
	)
	latest := 65.0

	h := newFallbackHandler(t, connectors.NewRegistry())

	// The cover came from a third site entirely; the chapter number from ComicK.
	h.setCachedCoverFromSource(buildCoverCacheKey("mangafire", primaryURL, nil),
		"https://cdn.example/cover.webp", "mangahub", true, time.Hour)
	h.setCachedChapterURL(buildChapterURLCacheKey("mangafire", primaryURL, latest),
		"", false, time.Hour)

	reporterID := int64(2)
	items := []models.Tracker{{
		ID: 1, Title: "Akanabe", Status: "reading", SourceID: 1,
		SourceURL:             primaryURL,
		LatestKnownChapter:    &latest,
		LatestChapterSourceID: &reporterID,
	}}
	sourceByID := map[int64]models.Source{
		1: {ID: 1, Key: "mangafire", Name: "MangaFire"},
		2: {ID: 2, Key: "comick", Name: "ComicK"},
		3: {ID: 3, Key: "mangahub", Name: "MangaHub"},
	}
	logos := map[int64]string{1: "/logos/mangafire.png", 2: "/logos/comick.png", 3: "/logos/mangahub.png"}
	alternates := map[int64][]repository.TrackerSourceRef{
		1: {
			{SourceID: 2, SourceKey: "comick", SourceURL: reporterURL},
			{SourceID: 3, SourceKey: "mangahub", SourceURL: artURL},
		},
	}

	cards, _ := h.buildTrackerCards(items, sourceByID, logos, alternates, "")
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	card := cards[0]

	if card.SourceLogoLabel != "ComicK" {
		t.Fatalf("expected the reporter to outrank the cover source, got %q", card.SourceLogoLabel)
	}
	if card.SourceURL != reporterURL {
		t.Fatalf("expected Open to follow the reporter, got %q", card.SourceURL)
	}
	if card.LatestKnownChapterURL != reporterURL {
		t.Fatalf("expected the degraded latest link to open the reporter's page, got %q", card.LatestKnownChapterURL)
	}
	if card.LatestKnownChapterSite != "ComicK" {
		t.Fatalf("expected the latest link labelled with the reporter, got %q", card.LatestKnownChapterSite)
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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceID: 9, SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	if _, err := h.fetchCoverURL(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, alternates); err != nil {
		t.Fatalf("expected the alternate to supply a cover: %v", err)
	}

	cacheKey := buildCoverCacheKey("blockedsource", "https://blocked.example/title/a", nil)
	_, servingKey, found, ok := h.getCachedCoverWithSource(cacheKey)
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

	h := newFallbackHandler(t, registry)

	if _, err := h.fetchCoverURL(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	cacheKey := buildCoverCacheKey("primarysource", "https://primary.example/title/a", nil)
	_, servingKey, _, _ := h.getCachedCoverWithSource(cacheKey)
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

	h := newFallbackHandler(t, registry)
	h.coverURLChecker = func(_ context.Context, coverURL string) bool {
		return !strings.Contains(coverURL, "dead.cdn")
	}
	alternates := []repository.TrackerSourceRef{
		{SourceID: 9, SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	coverURL, err := h.fetchCoverURL(context.Background(), "primarysource", "https://primary.example/title/a", nil, alternates)
	if err != nil {
		t.Fatalf("expected the alternate's loadable cover: %v", err)
	}
	if coverURL != "https://live.cdn.example/cover.webp" {
		t.Fatalf("expected the loadable cover to win, got %q", coverURL)
	}

	// The serving source must be the one whose image loads, so the badge
	// matches the picture.
	cacheKey := buildCoverCacheKey("primarysource", "https://primary.example/title/a", nil)
	_, servingKey, found, ok := h.getCachedCoverWithSource(cacheKey)
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

	h := newFallbackHandler(t, registry)
	h.coverURLChecker = func(context.Context, string) bool { return false }

	if _, err := h.fetchCoverURL(context.Background(), "primarysource", "https://primary.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error when no cover loads")
	}
	entry := negativeCoverEntry(t, h, buildCoverCacheKey("primarysource", "https://primary.example/title/a", nil))
	if remaining := time.Until(entry.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the retry negative TTL, got %s", remaining)
	}
}

func TestFetchCoverURLWithoutAlternatesStillFails(t *testing.T) {
	registry := connectors.NewRegistry()
	if err := registry.Register(blockedConnector{key: "blockedsource"}); err != nil {
		t.Fatalf("register blocked connector: %v", err)
	}

	h := newFallbackHandler(t, registry)

	if _, err := h.fetchCoverURL(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}
}

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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	chapterURL, err := h.fetchChapterURL("blockedsource", "https://blocked.example/title/a", 440.2, alternates)
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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	chapterURL, err := h.fetchChapterURL("primarysource", "https://primary.example/title/a", 12, alternates)
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

	h := newFallbackHandler(t, registry)

	if _, err := h.fetchChapterURL("blockedsource", "https://blocked.example/title/a", 5, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}

	cacheKey := buildChapterURLCacheKey("blockedsource", "https://blocked.example/title/a", 5)
	h.chapterURLCacheMu.RLock()
	entry, exists := h.chapterURLCache[cacheKey]
	h.chapterURLCacheMu.RUnlock()
	if !exists {
		t.Fatalf("expected a negative cache entry")
	}
	if remaining := time.Until(entry.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the retry negative TTL after a real attempt, got %s", remaining)
	}
}

// maxJitteredTTL is the ceiling jitteredTTL can return for a span, which is what
// a caller can assert against without depending on the random component.
func maxJitteredTTL(ttl time.Duration) time.Duration {
	return ttl + ttl/4
}

// TestFetchChapterURLUnknownConnectorCachesLonger pins the other half of that
// distinction: nothing was queried, so nothing will change soon.
func TestFetchChapterURLUnknownConnectorCachesLonger(t *testing.T) {
	h := newFallbackHandler(t, connectors.NewRegistry())

	if _, err := h.fetchChapterURL("nosuchsource", "https://nowhere.example/title/a", 5, nil); err == nil {
		t.Fatalf("expected an error for an unregistered connector")
	}

	cacheKey := buildChapterURLCacheKey("nosuchsource", "https://nowhere.example/title/a", 5)
	h.chapterURLCacheMu.RLock()
	entry, exists := h.chapterURLCache[cacheKey]
	h.chapterURLCacheMu.RUnlock()
	if !exists {
		t.Fatalf("expected a negative cache entry")
	}
	// Strictly longer than anything the retry span can produce, jitter included:
	// that gap is the whole point of the distinction.
	if remaining := time.Until(entry.ExpiresAt); remaining <= maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected a longer negative TTL when nothing was queried, got %s", remaining)
	}
}

func negativeCoverEntry(t *testing.T, h *DashboardHandler, cacheKey string) coverCacheEntry {
	t.Helper()
	h.cacheMu.RLock()
	entry, exists := h.coverCache[cacheKey]
	h.cacheMu.RUnlock()
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

	h := newFallbackHandler(t, registry)

	if _, err := h.fetchCoverURL(context.Background(), "blockedsource", "https://blocked.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error when the only source is blocked")
	}
	attempted := negativeCoverEntry(t, h, buildCoverCacheKey("blockedsource", "https://blocked.example/title/a", nil))
	if remaining := time.Until(attempted.ExpiresAt); remaining > maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected the retry negative TTL after a real attempt, got %s", remaining)
	}

	if _, err := h.fetchCoverURL(context.Background(), "nosuchsource", "https://nowhere.example/title/a", nil, nil); err == nil {
		t.Fatalf("expected an error for an unregistered connector")
	}
	unusable := negativeCoverEntry(t, h, buildCoverCacheKey("nosuchsource", "https://nowhere.example/title/a", nil))
	if remaining := time.Until(unusable.ExpiresAt); remaining <= maxJitteredTTL(lookupRetryTTL) {
		t.Fatalf("expected a longer negative TTL when nothing was queried, got %s", remaining)
	}
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

// blockedLinkableConnector is a blocked source that can still construct its
// reader URLs offline, the way the MangaFire connector can.
type blockedLinkableConnector struct{ blockedConnector }

func (b blockedLinkableConnector) BuildChapterURL(rawURL string, chapter float64) (string, bool) {
	return rawURL + "/read/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), true
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

	h := newFallbackHandler(t, registry)

	chapterURL, err := h.fetchChapterURL("blockedsource", "https://blocked.example/title/a", 177, nil)
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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a.ZD1"},
	}

	chapterURL, err := h.fetchChapterURL("blockedsource", "https://blocked.example/title/a", 12, alternates)
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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
	}

	chapterURL, err := h.fetchChapterURL("blockedsource", "https://blocked.example/title/a", 12, alternates)
	if err != nil {
		t.Fatalf("expected the built url to be served: %v", err)
	}
	if chapterURL != "https://blocked.example/title/a/read/chapter-12" {
		t.Fatalf("expected the blocked site's built url to beat the info floor, got %q", chapterURL)
	}
}

// linkableCappedConnector answers its resolver — refusing chapters beyond its
// latest with the typed verdict — and can also build reader URLs offline, the
// way the live MangaFire connector behaves when its site answers.
type linkableCappedConnector struct {
	cappedResolverConnector
}

func (l linkableCappedConnector) BuildChapterURL(rawURL string, chapter float64) (string, bool) {
	return rawURL + "/read/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), true
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

	h := newFallbackHandler(t, registry)
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
	}

	chapterURL, err := h.fetchChapterURL("answeredsource", "https://answered.example/title/a", 82, alternates)
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

	h := newFallbackHandler(t, registry)
	// Asura deliberately last in row order: its precedence must come from the
	// ranking, not from where the link scan happened to insert it.
	alternates := []repository.TrackerSourceRef{
		{SourceKey: "comick", SourceURL: "https://comick.example/comic/a"},
		{SourceKey: "mangahub", SourceURL: "https://mangahub.example/manga/a"},
		{SourceKey: "asuracomic", SourceURL: "https://asura.example/comics/a"},
	}

	chapterURL, err := h.fetchChapterURL("mangafire", "https://mangafire.example/title/a", 82, alternates)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if chapterURL != "https://asura.example/comics/a/chapter-82" {
		t.Fatalf("expected the origin site to win the chain, got %q", chapterURL)
	}
}

// cappedResolverConnector resolves chapter URLs only up to a latest-known
// number, the way the MangaHub connector's range check refuses chapters the
// site does not carry yet.
type cappedResolverConnector struct {
	mirrorConnector
	latest float64
}

func (c cappedResolverConnector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if chapter > c.latest {
		return "", fmt.Errorf("chapter beyond latest: %w", connectors.ErrChapterNotFound)
	}
	return c.mirrorConnector.ResolveChapterURL(ctx, rawURL, chapter)
}

// newReaderPriorityHandler wires the three sources of the reading chain the
// way a MangaFire-primary tracker carries them: MangaFire primary
// (challenge-blocked, offline-linkable), ComicK and MangaHub alternates —
// deliberately in that linked order, since ComicK was linked first in
// production and the priority must come from ranking, not row order.
func newReaderPriorityHandler(t *testing.T, mangahubLatest float64) (*DashboardHandler, []repository.TrackerSourceRef) {
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
	return newFallbackHandler(t, registry), alternates
}

// TestFetchChapterURLPrefersFreshMangaHub pins step 1 of the reading chain:
// when MangaHub carries the requested chapter, reading happens there, even
// though the MangaFire primary could build a link and ComicK could resolve.
func TestFetchChapterURLPrefersFreshMangaHub(t *testing.T) {
	h, alternates := newReaderPriorityHandler(t, 100)

	chapterURL, err := h.fetchChapterURL("mangafire", "https://mangafire.example/title/a", 99, alternates)
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
	h, alternates := newReaderPriorityHandler(t, 100)

	chapterURL, err := h.fetchChapterURL("mangafire", "https://mangafire.example/title/a", 101, alternates)
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
	h, alternates := newReaderPriorityHandler(t, 100)

	// The tracker's primary is ComicK here — no MangaFire in the chain.
	chapterURL, err := h.fetchChapterURL("comick", "https://comick.example/comic/a", 101, alternates[1:])
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
	handler := &DashboardHandler{registry: connectordefaults.NewRegistry()}
	candidates := []repository.TrackerSourceRef{
		{SourceKey: "mangafire"},
		{SourceKey: "mangaupdates"},
		{SourceKey: "comick"},
		{SourceKey: "asuracomic"},
		{SourceKey: "mangadex"},
		{SourceKey: "mangahub"},
		{SourceKey: "flamecomics"},
	}
	orderReaderCandidates(candidates, handler.readerCandidateRank)

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

// TestPinnedReadingRef pins the pin semantics: primary, alternate, stale.
func TestPinnedReadingRef(t *testing.T) {
	primaryID := int64(7)
	alternateID := int64(9)
	staleID := int64(99)
	itemID := "abc"
	alternates := []repository.TrackerSourceRef{
		{SourceID: alternateID, SourceKey: "mirrorsource", SourceURL: "https://mirror.example/series/a"},
	}
	tracker := models.Tracker{ID: 1, SourceID: primaryID, SourceItemID: &itemID, SourceURL: "https://primary.example/title/a"}

	if got := pinnedReadingRef(tracker, "primarysource", alternates); got != nil {
		t.Fatalf("no pin must mean auto, got %+v", got)
	}

	tracker.ReadingSourceID = &primaryID
	got := pinnedReadingRef(tracker, "primarysource", alternates)
	if got == nil || got.SourceKey != "primarysource" || got.SourceURL != tracker.SourceURL {
		t.Fatalf("primary pin = %+v", got)
	}

	tracker.ReadingSourceID = &alternateID
	got = pinnedReadingRef(tracker, "primarysource", alternates)
	if got == nil || got.SourceKey != "mirrorsource" {
		t.Fatalf("alternate pin = %+v", got)
	}

	// A pin to a source the tracker no longer carries degrades to auto.
	tracker.ReadingSourceID = &staleID
	if got := pinnedReadingRef(tracker, "primarysource", alternates); got != nil {
		t.Fatalf("stale pin must degrade to auto, got %+v", got)
	}
}
