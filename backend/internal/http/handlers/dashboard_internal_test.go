package handlers

import (
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

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
	homeURL := sourceHomeURLForKey("mgeko")
	if homeURL != "https://www.mgeko.cc" {
		t.Fatalf("expected mgeko home url, got %q", homeURL)
	}
}

func TestInferSourceKeyFromURLSupportsMgeko(t *testing.T) {
	inferred := inferSourceKeyFromURL("https://www.mgeko.cc/manga/sample-series/")
	if inferred != "mgeko" {
		t.Fatalf("expected inferred source key mgeko, got %q", inferred)
	}
}

func TestSourceHomeURLForKeySupportsFreeWebNovel(t *testing.T) {
	homeURL := sourceHomeURLForKey("freewebnovel")
	if homeURL != "https://freewebnovel.com" {
		t.Fatalf("expected freewebnovel home url, got %q", homeURL)
	}
}

func TestInferSourceKeyFromURLSupportsFreeWebNovel(t *testing.T) {
	inferred := inferSourceKeyFromURL("https://freewebnovel.com/novel/star-odyssey")
	if inferred != "freewebnovel" {
		t.Fatalf("expected inferred source key freewebnovel, got %q", inferred)
	}
}

// TestInferSourceKeyFromURLSupportsMangaBuddy pins the gap that shipped with the
// MangaBuddy connector: sourceHomeURLForKey knew the source but this did not, so
// a cover fallback could not recognise a MangaBuddy URL as one it could serve.
// The live host carries a numeral, which is exactly the shape a substring match
// has to survive.
func TestInferSourceKeyFromURLSupportsMangaBuddy(t *testing.T) {
	inferred := inferSourceKeyFromURL("https://mangabuddy1.co.uk/manga/sample-series")
	if inferred != "mangabuddy" {
		t.Fatalf("expected inferred source key mangabuddy, got %q", inferred)
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
