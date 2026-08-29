package handlers

import (
	"testing"
	"time"

	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

// newSiteMetadataHandler builds a handler around the connector set the app
// actually runs, which is where the site metadata below now comes from: these
// tests exist to catch a connector that stops claiming a domain or moves its
// home page, so a stub registry would test nothing.
func newSiteMetadataHandler() *DashboardHandler {
	return &DashboardHandler{registry: connectordefaults.NewRegistry()}
}

func TestHasResolvedSourceMetadataRequiresReleaseDate(t *testing.T) {
	chapter := 42.0
	itemID := "mangadex-item"
	now := time.Now().UTC()

	trackerWithoutRelease := &models.Tracker{
		SourceItemID:       &itemID,
		LatestKnownChapter: &chapter,
	}
	if hasResolvedSourceMetadata(trackerWithoutRelease) {
		t.Fatalf("expected metadata without latest release date to be incomplete")
	}

	trackerWithRelease := &models.Tracker{
		SourceItemID:       &itemID,
		LatestKnownChapter: &chapter,
		LatestReleaseAt:    &now,
	}
	if !hasResolvedSourceMetadata(trackerWithRelease) {
		t.Fatalf("expected metadata with source item id, latest chapter, and release date to be complete")
	}
}

func TestSourceHomeURLForKeySupportsMgeko(t *testing.T) {
	homeURL := newSiteMetadataHandler().sourceHomeURLForKey("mgeko")
	if homeURL != "https://www.mgeko.cc" {
		t.Fatalf("expected mgeko home url, got %q", homeURL)
	}
}

func TestSourceKeyForURLSupportsMgeko(t *testing.T) {
	inferred := newSiteMetadataHandler().sourceKeyForURL("https://www.mgeko.cc/manga/sample-series/")
	if inferred != "mgeko" {
		t.Fatalf("expected inferred source key mgeko, got %q", inferred)
	}
}

func TestSourceHomeURLForKeySupportsFreeWebNovel(t *testing.T) {
	homeURL := newSiteMetadataHandler().sourceHomeURLForKey("freewebnovel")
	if homeURL != "https://freewebnovel.com" {
		t.Fatalf("expected freewebnovel home url, got %q", homeURL)
	}
}

func TestSourceKeyForURLSupportsFreeWebNovel(t *testing.T) {
	inferred := newSiteMetadataHandler().sourceKeyForURL("https://freewebnovel.com/novel/star-odyssey")
	if inferred != "freewebnovel" {
		t.Fatalf("expected inferred source key freewebnovel, got %q", inferred)
	}
}

// TestSourceKeyForURLKnowsEveryChapterSource pins a gap that has now shipped
// twice (MangaBuddy, then ComicK/MangaHub): a connector gets wired everywhere
// except here, and then a resolved chapter link cannot be attributed to its
// site — which silently breaks the open-to-read button following the freshest
// source.
func TestSourceKeyForURLKnowsEveryChapterSource(t *testing.T) {
	handler := newSiteMetadataHandler()
	cases := map[string]string{
		"https://mangahub.io/manga/sample-series_142": "mangahub",
		"https://comick.dev/comic/kagura-bachi":       "comick",
		"https://mangafire.to/title/npozj-akanabe":    "mangafire",
	}
	for rawURL, want := range cases {
		if inferred := handler.sourceKeyForURL(rawURL); inferred != want {
			t.Fatalf("expected inferred source key %q for %s, got %q", want, rawURL, inferred)
		}
	}
}

// TestSourceKeyForURLRejectsALookalikeHost covers what the substring table this
// replaced could not: a host that merely reads like a site's name is not that
// site, and attributing a card to it would name the wrong reader.
func TestSourceKeyForURLRejectsALookalikeHost(t *testing.T) {
	handler := newSiteMetadataHandler()
	for _, rawURL := range []string{
		"https://asura-mirror.example/comics/sample",
		"https://comick.example/comic/sample",
	} {
		if inferred := handler.sourceKeyForURL(rawURL); inferred != "" {
			t.Fatalf("expected no source key for %s, got %q", rawURL, inferred)
		}
	}
}

// TestSourceKeyForURLWithoutARegistry covers the handlers the card builders
// construct by struct literal: no registry means nothing is attributed, not a
// panic.
func TestSourceKeyForURLWithoutARegistry(t *testing.T) {
	handler := &DashboardHandler{}
	if inferred := handler.sourceKeyForURL("https://mangahub.io/manga/sample-series_142"); inferred != "" {
		t.Fatalf("expected no source key without a registry, got %q", inferred)
	}
	if homeURL := handler.sourceHomeURLForKey("mangahub"); homeURL != "" {
		t.Fatalf("expected no home url without a registry, got %q", homeURL)
	}
}

func TestBuildTrackerCardsDoesNotUseLastCheckedAtAsReleaseDate(t *testing.T) {
	lastCheckedAt := time.Now().UTC()
	h := &DashboardHandler{}
	items := []models.Tracker{{
		ID:            1,
		Title:         "Sample",
		Status:        "reading",
		SourceID:      1,
		SourceURL:     "https://example.com/series/sample",
		LastCheckedAt: &lastCheckedAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, pending := h.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, "")
	if pending {
		t.Fatalf("expected no asynchronous lookups for source without connector key")
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "—" {
		t.Fatalf("expected unknown release date marker, got %q", cards[0].LatestReleaseAgo)
	}
}

// TestBuildTrackerCardsShowsChapterSeenAtAsApproximateRelease covers the sources
// that report a chapter number and no date for it, where the card used to show only
// "—": it now falls back to when the chapter was first recorded here, marked as
// approximate so a detection time is never read as the site's release time.
func TestBuildTrackerCardsShowsChapterSeenAtAsApproximateRelease(t *testing.T) {
	chapter := 20.0
	seenAt := time.Now().UTC().Add(-48 * time.Hour)
	h := &DashboardHandler{}
	items := []models.Tracker{{
		ID:                  1,
		Title:               "Sample",
		Status:              "completed",
		SourceID:            1,
		SourceURL:           "https://example.com/series/sample",
		LatestKnownChapter:  &chapter,
		LatestChapterSeenAt: &seenAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, _ := h.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, "")
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "~2 days ago" {
		t.Fatalf("expected approximate release date from first-seen timestamp, got %q", cards[0].LatestReleaseAgo)
	}
	if !cards[0].LatestReleaseApproximate {
		t.Fatalf("expected the release date to be marked approximate")
	}
	if cards[0].LatestReleaseTitle == "" {
		t.Fatalf("expected an explanation of where the approximate date came from")
	}
}

// TestBuildTrackerCardsPrefersReportedReleaseDate keeps the fallback above from
// overriding a date a source actually reported.
func TestBuildTrackerCardsPrefersReportedReleaseDate(t *testing.T) {
	chapter := 20.0
	releasedAt := time.Now().UTC().Add(-2 * time.Hour)
	seenAt := time.Now().UTC().Add(-48 * time.Hour)
	h := &DashboardHandler{}
	items := []models.Tracker{{
		ID:                  1,
		Title:               "Sample",
		Status:              "reading",
		SourceID:            1,
		SourceURL:           "https://example.com/series/sample",
		LatestKnownChapter:  &chapter,
		LatestReleaseAt:     &releasedAt,
		LatestChapterSeenAt: &seenAt,
	}}
	sourceByID := map[int64]models.Source{1: {ID: 1, Name: "Example"}}

	cards, _ := h.buildTrackerCards(items, sourceByID, map[int64]string{}, nil, "")
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].LatestReleaseAgo != "2 hours ago" {
		t.Fatalf("expected the reported release date, got %q", cards[0].LatestReleaseAgo)
	}
	if cards[0].LatestReleaseApproximate {
		t.Fatalf("did not expect a reported release date to be marked approximate")
	}
}
