package handlers

import (
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// The link arbitration decides which site a card sends the reader to, and it
// used to be reachable only through a rendered page, so every rule below had to
// be trusted rather than checked. They are pinned here one at a time.

const (
	primarySeriesURL   = "https://www.mangaupdates.com/series/abc/sample"
	mangafireSeriesURL = "https://mangafire.to/manga/sample.abc"
	comickSeriesURL    = "https://comick.dev/comic/sample"
)

// testSourceKeyForURL stands in for the lookup the handler injects, which in
// the running app reads the hosts the connectors claim. It is spelled out here
// so the policy below is pinned on its own: which site a link is attributed to
// is a separate question from which connectors happen to be registered.
func testSourceKeyForURL(rawURL string) string {
	switch {
	case strings.Contains(rawURL, "mangafire.to"):
		return "mangafire"
	case strings.Contains(rawURL, "comick.dev"):
		return "comick"
	case strings.Contains(rawURL, "mangaupdates.com"):
		return "mangaupdates"
	default:
		return ""
	}
}

func sampleAlternates() []repository.TrackerSourceRef {
	return []repository.TrackerSourceRef{
		{SourceID: 2, SourceKey: "mangafire", SourceURL: mangafireSeriesURL},
		{SourceID: 3, SourceKey: "comick", SourceURL: comickSeriesURL},
	}
}

func TestDecideTrackerLinksFallsBackToTheReadingBase(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey: "mangaupdates",
		PrimarySourceURL: primarySeriesURL,
	})

	if decision.LatestChapterURL != primarySeriesURL {
		t.Fatalf("expected the latest-chapter link to stay on the series page, got %q", decision.LatestChapterURL)
	}
	if decision.LastReadChapterURL != primarySeriesURL {
		t.Fatalf("expected the last-read link to stay on the series page, got %q", decision.LastReadChapterURL)
	}
	if decision.HighlightURL != primarySeriesURL {
		t.Fatalf("expected the open-to-read button to stay on the series page, got %q", decision.HighlightURL)
	}
	if decision.LatestChapterSiteKey != "" || decision.LastReadChapterSiteKey != "" {
		t.Fatalf("expected no site labels when nothing resolved, got %q and %q", decision.LatestChapterSiteKey, decision.LastReadChapterSiteKey)
	}
	if decision.ServingSourceKey != "" {
		t.Fatalf("expected no serving source, got %q", decision.ServingSourceKey)
	}
	if decision.ServingSourceFound {
		t.Fatalf("expected the card to keep presenting its primary source")
	}
}

// TestDecideTrackerLinksPrefersTheConfirmedChapterSite is the top of the badge
// precedence: a confirmed chapter link outranks both the reporter and the cover.
func TestDecideTrackerLinksPrefersTheConfirmedChapterSite(t *testing.T) {
	alternates := sampleAlternates()
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Alternates:        alternates,
		LatestChapter:     chapterLinkLookup{Attempted: true, URL: "https://mangafire.to/title/abc-sample/chapter/82", Resolved: true},
		ReporterSourceKey: "comick",
		CoverSourceKey:    "comick",
		SourceKeyForURL:   testSourceKeyForURL,
	})

	if decision.LatestChapterSiteKey != "mangafire" {
		t.Fatalf("expected the latest-chapter link labelled with the site it opens, got %q", decision.LatestChapterSiteKey)
	}
	if decision.ServingSourceKey != "mangafire" {
		t.Fatalf("expected the confirmed chapter site to win the badge, got %q", decision.ServingSourceKey)
	}
	if !decision.ServingSourceFound || decision.ServingSource.SourceID != 2 {
		t.Fatalf("expected the badge resolved to the linked MangaFire alternate, got %+v", decision.ServingSource)
	}
	if decision.HighlightURL != mangafireSeriesURL {
		t.Fatalf("expected the open-to-read button to follow the chapter link's site, got %q", decision.HighlightURL)
	}
}

// TestDecideTrackerLinksFallsBackToTheReporterForTheBadge is the middle of the
// precedence: nothing was confirmed, so the site that claimed the chapter number
// exists is the best remaining signal.
func TestDecideTrackerLinksFallsBackToTheReporterForTheBadge(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Alternates:        sampleAlternates(),
		LatestChapter:     chapterLinkLookup{Attempted: true, URL: primarySeriesURL},
		ReporterSourceKey: "mangafire",
		CoverSourceKey:    "comick",
	})

	if decision.ServingSourceKey != "mangafire" {
		t.Fatalf("expected the reporter to outrank the cover source, got %q", decision.ServingSourceKey)
	}
	if !decision.ServingSourceFound || decision.ServingSource.SourceID != 2 {
		t.Fatalf("expected the badge resolved to the reporter's linked alternate, got %+v", decision.ServingSource)
	}
}

// TestDecideTrackerLinksFallsBackToTheCoverSourceForTheBadge is the bottom of
// the precedence: the weakest signal on the card still beats badging a primary
// that served nothing.
func TestDecideTrackerLinksFallsBackToTheCoverSourceForTheBadge(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey: "mangaupdates",
		PrimarySourceURL: primarySeriesURL,
		Alternates:       sampleAlternates(),
		CoverSourceKey:   "comick",
	})

	if decision.ServingSourceKey != "comick" {
		t.Fatalf("expected the cover source to serve the badge, got %q", decision.ServingSourceKey)
	}
	if !decision.ServingSourceFound || decision.ServingSource.SourceID != 3 {
		t.Fatalf("expected the badge resolved to the linked ComicK alternate, got %+v", decision.ServingSource)
	}
}

// TestDecideTrackerLinksKeepsThePrimaryBadgeWhenItServedTheCard guards the case
// the badge must not move: the primary itself supplied the card.
func TestDecideTrackerLinksKeepsThePrimaryBadgeWhenItServedTheCard(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey: "mangafire",
		PrimarySourceURL: mangafireSeriesURL,
		Alternates:       sampleAlternates(),
		LatestChapter:    chapterLinkLookup{Attempted: true, URL: "https://mangafire.to/title/abc-sample/chapter/82", Resolved: true},
		SourceKeyForURL:  testSourceKeyForURL,
	})

	if decision.ServingSourceKey != "mangafire" {
		t.Fatalf("expected the primary to be named as the serving source, got %q", decision.ServingSourceKey)
	}
	if decision.ServingSourceFound {
		t.Fatalf("expected no badge override when the primary served the card")
	}
	if decision.HighlightURL != mangafireSeriesURL {
		t.Fatalf("expected the open-to-read button to stay on the primary series page, got %q", decision.HighlightURL)
	}
}

// TestDecideTrackerLinksDegradesToTheReporterSeriesPage covers the link the user
// actually clicks when no site could confirm the chapter: the reporter's page,
// where the number on the card came from.
func TestDecideTrackerLinksDegradesToTheReporterSeriesPage(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Alternates:        sampleAlternates(),
		LatestChapter:     chapterLinkLookup{Attempted: true, URL: primarySeriesURL},
		ReporterSourceKey: "comick",
	})

	if decision.LatestChapterURL != comickSeriesURL {
		t.Fatalf("expected the latest-chapter link to degrade to the reporter's series page, got %q", decision.LatestChapterURL)
	}
	if decision.LatestChapterSiteKey != "comick" {
		t.Fatalf("expected the link labelled with the reporter's site, got %q", decision.LatestChapterSiteKey)
	}
	// A degraded link confirmed nothing, so it does not move the open-to-read
	// button even though the badge follows the reporter.
	if decision.HighlightURL != primarySeriesURL {
		t.Fatalf("expected the open-to-read button to stay on the primary series page, got %q", decision.HighlightURL)
	}
}

// TestDecideTrackerLinksKeepsTheDegradedLinkWithoutALinkedReporter covers a
// reporter the tracker does not carry: there is no page to send the user to, so
// the degraded link stands.
func TestDecideTrackerLinksKeepsTheDegradedLinkWithoutALinkedReporter(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Alternates:        []repository.TrackerSourceRef{{SourceID: 4, SourceKey: "mangadex", SourceURL: ""}},
		LatestChapter:     chapterLinkLookup{Attempted: true, URL: primarySeriesURL},
		ReporterSourceKey: "mangadex",
	})

	if decision.LatestChapterURL != primarySeriesURL {
		t.Fatalf("expected the degraded link to stand when the reporter has no linked page, got %q", decision.LatestChapterURL)
	}
	if decision.LatestChapterSiteKey != "" {
		t.Fatalf("expected no site label for a link that was never retargeted, got %q", decision.LatestChapterSiteKey)
	}
}

// TestDecideTrackerLinksPinNarrowsEveryLinkToOneSite: a pin is a promise that
// the reader is only ever sent to that site, so neither the reporter fallback
// nor the open-to-read retarget may point elsewhere.
func TestDecideTrackerLinksPinNarrowsEveryLinkToOneSite(t *testing.T) {
	pinned := repository.TrackerSourceRef{SourceID: 3, SourceKey: "comick", SourceURL: comickSeriesURL}
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Pinned:            &pinned,
		Alternates:        sampleAlternates(),
		LatestChapter:     chapterLinkLookup{Attempted: true, URL: comickSeriesURL},
		ReporterSourceKey: "mangafire",
	})

	if decision.LatestChapterURL != comickSeriesURL {
		t.Fatalf("expected the pinned site's page rather than the reporter's, got %q", decision.LatestChapterURL)
	}
	if decision.LatestChapterSiteKey != "" {
		t.Fatalf("expected no site label for an unconfirmed pinned link, got %q", decision.LatestChapterSiteKey)
	}
	if decision.HighlightURL != comickSeriesURL {
		t.Fatalf("expected the open-to-read button on the pinned site, got %q", decision.HighlightURL)
	}
	if decision.LastReadChapterURL != comickSeriesURL {
		t.Fatalf("expected the last-read link on the pinned site, got %q", decision.LastReadChapterURL)
	}
}

// TestDecideTrackerLinksPinnedChapterLinkStillSetsTheBadge keeps a pin from
// suppressing the badge: the links are narrowed, but the card still says which
// site is serving it.
func TestDecideTrackerLinksPinnedChapterLinkStillSetsTheBadge(t *testing.T) {
	pinned := repository.TrackerSourceRef{SourceID: 2, SourceKey: "mangafire", SourceURL: mangafireSeriesURL}
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey: "mangaupdates",
		PrimarySourceURL: primarySeriesURL,
		Pinned:           &pinned,
		Alternates:       sampleAlternates(),
		LatestChapter:    chapterLinkLookup{Attempted: true, URL: "https://mangafire.to/title/abc-sample/chapter/82", Resolved: true},
		SourceKeyForURL:  testSourceKeyForURL,
	})

	if decision.ServingSourceKey != "mangafire" {
		t.Fatalf("expected the pinned site to serve the badge, got %q", decision.ServingSourceKey)
	}
	if !decision.ServingSourceFound || decision.ServingSource.SourceID != 2 {
		t.Fatalf("expected the badge resolved to the pinned alternate, got %+v", decision.ServingSource)
	}
}

// TestDecideTrackerLinksLastReadLinkIsIndependent: the last-read link gets no
// reporter fallback — the reporter's claim is about the newest chapter, not
// about the one the user stopped at.
func TestDecideTrackerLinksLastReadLinkIsIndependent(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Alternates:        sampleAlternates(),
		LastReadChapter:   chapterLinkLookup{Attempted: true, URL: primarySeriesURL},
		ReporterSourceKey: "comick",
	})

	if decision.LastReadChapterURL != primarySeriesURL {
		t.Fatalf("expected the last-read link to stay on the series page, got %q", decision.LastReadChapterURL)
	}
	if decision.LastReadChapterSiteKey != "" {
		t.Fatalf("expected no site label for an unconfirmed last-read link, got %q", decision.LastReadChapterSiteKey)
	}

	resolved := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey: "mangaupdates",
		PrimarySourceURL: primarySeriesURL,
		Alternates:       sampleAlternates(),
		LastReadChapter:  chapterLinkLookup{Attempted: true, URL: "https://comick.dev/comic/sample/chapter-12", Resolved: true},
		SourceKeyForURL:  testSourceKeyForURL,
	})

	if resolved.LastReadChapterSiteKey != "comick" {
		t.Fatalf("expected the last-read link labelled with the site it opens, got %q", resolved.LastReadChapterSiteKey)
	}
	// The last-read link says where it goes, but it is not evidence about the
	// newest chapter, so it never wins the badge.
	if resolved.ServingSourceKey != "" {
		t.Fatalf("expected the last-read link not to decide the badge, got %q", resolved.ServingSourceKey)
	}
}

// TestDecideTrackerLinksWithoutASiteLookupNamesNoSite pins the default the
// arbitration keeps when no lookup is supplied: links still resolve, but
// nothing is attributed to a site — the same answer an unknown host gets.
func TestDecideTrackerLinksWithoutASiteLookupNamesNoSite(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey: "mangaupdates",
		PrimarySourceURL: primarySeriesURL,
		Alternates:       sampleAlternates(),
		LatestChapter:    chapterLinkLookup{Attempted: true, URL: "https://mangafire.to/title/abc-sample/chapter/82", Resolved: true},
	})

	if decision.LatestChapterURL != "https://mangafire.to/title/abc-sample/chapter/82" {
		t.Fatalf("expected the confirmed link to stand, got %q", decision.LatestChapterURL)
	}
	if decision.LatestChapterSiteKey != "" {
		t.Fatalf("expected no site label without a lookup, got %q", decision.LatestChapterSiteKey)
	}
	if decision.HighlightURL != primarySeriesURL {
		t.Fatalf("expected the open-to-read button unmoved without a lookup, got %q", decision.HighlightURL)
	}
}

// TestDecideTrackerLinksIgnoresAnUnrecognisedChapterHost: a confirmed link on a
// host no connector claims cannot be attributed, and a card that named the wrong
// site would be worse than one that names none.
func TestDecideTrackerLinksIgnoresAnUnrecognisedChapterHost(t *testing.T) {
	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  "mangaupdates",
		PrimarySourceURL:  primarySeriesURL,
		Alternates:        sampleAlternates(),
		LatestChapter:     chapterLinkLookup{Attempted: true, URL: "https://unknown-mirror.example/read/sample/82", Resolved: true},
		ReporterSourceKey: "comick",
		SourceKeyForURL:   testSourceKeyForURL,
	})

	if decision.LatestChapterURL != "https://unknown-mirror.example/read/sample/82" {
		t.Fatalf("expected the confirmed link to stand, got %q", decision.LatestChapterURL)
	}
	if decision.LatestChapterSiteKey != "" {
		t.Fatalf("expected no site label for an unattributable host, got %q", decision.LatestChapterSiteKey)
	}
	if decision.HighlightURL != primarySeriesURL {
		t.Fatalf("expected the open-to-read button unmoved by an unattributable host, got %q", decision.HighlightURL)
	}
	if decision.ServingSourceKey != "comick" {
		t.Fatalf("expected the badge to fall through to the reporter, got %q", decision.ServingSourceKey)
	}
}
