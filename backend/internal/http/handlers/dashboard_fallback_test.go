package handlers

import (
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// The resolvers themselves — the caches, the fallback chains, the TTLs — are
// pinned in internal/resolve. What is left here is the card: which site it ends
// up presenting given what those lookups answered. The doubles below supply the
// answers directly, so a card's presentation is exercised without a cache, a
// connector or a clock.

type fakeCoverResolver struct {
	coverURL  string
	sourceKey string
	waiting   bool
}

func (f fakeCoverResolver) Lookup(string, string, *string, []repository.TrackerSourceRef, string) (string, string, bool) {
	return f.coverURL, f.sourceKey, f.waiting
}

func (fakeCoverResolver) InvalidateNegatives() {}
func (fakeCoverResolver) Close()               {}

type fakeChapterAnswer struct {
	url      string
	resolved bool
}

type fakeChapterLinkResolver struct {
	byChapter map[float64]fakeChapterAnswer
	waiting   bool
}

// Lookup degrades a chapter it holds no answer for to the series page, which is
// what the real resolver hands back for a lookup that resolved nothing.
func (f fakeChapterLinkResolver) Lookup(_ string, sourceURL string, chapter float64, _ []repository.TrackerSourceRef, _ string) (string, bool, bool) {
	answer, ok := f.byChapter[chapter]
	if !ok {
		return sourceURL, false, f.waiting
	}
	return answer.url, answer.resolved, f.waiting
}

func (fakeChapterLinkResolver) Invalidate() {}
func (fakeChapterLinkResolver) Close()      {}

// newCardTestHandler names the three collaborators the card builder actually
// reads: the registry that attributes a resolved link to a site, and the two
// resolvers whose answers the card is built from.
func newCardTestHandler(registry *connectors.Registry, covers coverResolver, chapters chapterLinkResolver) *DashboardHandler {
	return &DashboardHandler{registry: registry, covers: covers, chapterLinks: chapters}
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

// mixedSourceCard builds the card for a tracker whose cover came from a mirror.
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
	// checked on here.
	handler := newCardTestHandler(
		connectordefaults.NewRegistry(),
		// The cover came from the mirror: the primary's art endpoint was
		// unreachable.
		fakeCoverResolver{coverURL: "https://cdn.example/cover.webp", sourceKey: "comick"},
		fakeChapterLinkResolver{byChapter: map[float64]fakeChapterAnswer{
			latest: {url: latestChapterURL, resolved: latestResolved},
			// The older chapter sits deep in the primary's paged listing and
			// only the mirror could answer for it.
			lastRead: {url: mirrorURL + "/chapter-59", resolved: true},
		}},
	)

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

	cards, pending := handler.buildTrackerCards(items, sourceByID, logos, alternates, "")
	if pending {
		t.Fatalf("expected every lookup to be answered")
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
	// Nothing resolved: the link degrades to the series page, which names the
	// primary but proves nothing about who can serve the chapter.
	card := mixedSourceCard(t, "https://mangafire.to/title/npozj-akanabe", false)

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

	// The cover came from a third site entirely; the chapter number from ComicK,
	// and no chapter link resolved anywhere.
	handler := newCardTestHandler(
		connectors.NewRegistry(),
		fakeCoverResolver{coverURL: "https://cdn.example/cover.webp", sourceKey: "mangahub"},
		fakeChapterLinkResolver{byChapter: map[float64]fakeChapterAnswer{
			latest: {url: primaryURL},
		}},
	)

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

	cards, _ := handler.buildTrackerCards(items, sourceByID, logos, alternates, "")
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

// TestBuildTrackerCardsReportsAPendingLookup pins what keeps the page asking for
// a corrected render: a lookup that has not answered yet must be reported, or
// the first render's placeholder art is the last thing the reader sees.
func TestBuildTrackerCardsReportsAPendingLookup(t *testing.T) {
	handler := newCardTestHandler(
		connectors.NewRegistry(),
		fakeCoverResolver{waiting: true},
		fakeChapterLinkResolver{},
	)

	items := []models.Tracker{{
		ID: 1, Title: "Akanabe", Status: "reading", SourceID: 1,
		SourceURL: "https://mangafire.to/title/npozj-akanabe",
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Key: "mangafire", Name: "MangaFire"}}

	if _, pending := handler.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, ""); !pending {
		t.Fatalf("expected a pending cover lookup to be reported")
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
