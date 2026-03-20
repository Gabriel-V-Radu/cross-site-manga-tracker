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

	cards, pending := h.buildTrackerCards(items, sourceByID, map[int64]string{}, "")
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
