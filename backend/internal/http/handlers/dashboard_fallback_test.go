package handlers

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// blockedConnector stands in for a source behind a bot challenge: it is
// registered and reachable in principle, but every resolve fails.
type blockedConnector struct{ key string }

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
	if remaining := time.Until(entry.ExpiresAt); remaining > 5*time.Minute {
		t.Fatalf("expected a short negative TTL after a real attempt, got %s", remaining)
	}
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
	if remaining := time.Until(entry.ExpiresAt); remaining < 5*time.Minute {
		t.Fatalf("expected a long negative TTL when nothing was queried, got %s", remaining)
	}
}
