package linkscan

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// linkingConnector is a stubConnector whose pages publish cross links, the
// way MangaFire's title records name the series' MangaDex and MangaUpdates
// pages.
type linkingConnector struct {
	*stubConnector
	links    map[string]map[string]string
	keys     []string
	linkAsks []string
}

func (c *linkingConnector) CrossLinks(_ context.Context, rawURL string) (map[string]string, error) {
	c.linkAsks = append(c.linkAsks, rawURL)
	if links, ok := c.links[rawURL]; ok {
		return links, nil
	}
	return nil, fmt.Errorf("unreachable")
}

func (c *linkingConnector) CrossLinkKeys() []string { return c.keys }

// trailingConnector is a stubConnector the poller only falls back to, like
// MangaUpdates.
type trailingConnector struct{ *stubConnector }

func (c *trailingConnector) TrailingChapterFeed() bool { return true }

func newMangaFire(links map[string]map[string]string) *linkingConnector {
	return &linkingConnector{
		stubConnector: &stubConnector{key: "mangafire"},
		links:         links,
		keys:          []string{"mangabaka", "mangadex", "mangaupdates"},
	}
}

func chapter(value float64) *float64 { return &value }

// runScanOn scans the given connector with others registered beside it.
func runScanOn(t *testing.T, store *stubStore, scanned connectors.Connector, aid AidLookup, others ...connectors.Connector) *Scanner {
	t.Helper()
	registry := connectors.NewRegistry()
	for _, connector := range append([]connectors.Connector{scanned}, others...) {
		if err := registry.Register(connector); err != nil {
			t.Fatalf("register %s: %v", connector.Key(), err)
		}
	}
	scanner := NewScanner(store, registry, aid, slog.Default())
	if err := scanner.Start(1, 42, scanned.Key(), scanned.Name(), repository.LinkScanFilter{}); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	waitForScanEnd(t, scanner, 1)
	return scanner
}

const (
	mangaFireURL  = "https://mangafire.to/title/abc12-hero-organization"
	mangaDexURL   = "https://mangadex.org/title/ce398cd4-33cf-4717-9e2c-ef98c7a5c578"
	mangaDexOther = "https://mangadex.org/title/00000000-0000-0000-0000-000000000000"
)

func mangaFireTarget(latest float64) repository.LinkScanTarget {
	return repository.LinkScanTarget{
		TrackerID:          1,
		Title:              "Hero Organization",
		LatestKnownChapter: chapter(latest),
		Links:              []repository.TrackerLink{{SourceID: 3, SourceKey: "mangafire", URL: mangaFireURL}},
	}
}

// A linked site naming the series' page on the scanned site makes that page
// the one exact candidate, without a title search.
func TestCrossLinkMakesTheExactCandidate(t *testing.T) {
	mangaFire := newMangaFire(map[string]map[string]string{mangaFireURL: {"mangadex": mangaDexURL}})
	mangaDex := &stubConnector{key: "mangadex", resolved: map[string]*connectors.MangaResult{
		mangaDexURL: {Title: "Eiyuu Soshiki", URL: mangaDexURL, LatestChapter: chapter(75)},
	}}
	store := newStubStore(mangaFireTarget(75))

	runScanOn(t, store, mangaDex, nil, mangaFire)

	stored := store.stored[1]
	if len(stored) != 1 || stored[0].CandidateURL != mangaDexURL || stored[0].Score != 1.0 || stored[0].Basis != "mangafire" {
		t.Fatalf("stored = %+v, want the one exact MangaDex page with MangaFire as basis", stored)
	}
	if len(mangaDex.searchCalls) != 0 {
		t.Fatalf("searched %v; an id match needs no search", mangaDex.searchCalls)
	}
}

// A site that takes turns in the rotation and is behind the tracker would make
// new chapters slower to find: its id match is still offered, but not as
// exact. A trailing feed lagging behind costs nothing and stays exact.
func TestCrossLinkBehindIsNotExactOnARotatingSite(t *testing.T) {
	for name, tc := range map[string]struct {
		latest    *float64
		trailing  bool
		wantScore float64
	}{
		"behind":            {latest: chapter(114), wantScore: behindIDScore},
		"no english":        {latest: nil, wantScore: behindIDScore},
		"one behind":        {latest: chapter(541), wantScore: 1.0},
		"behind but a feed": {latest: chapter(114), trailing: true, wantScore: 1.0},
	} {
		t.Run(name, func(t *testing.T) {
			mangaFire := newMangaFire(map[string]map[string]string{mangaFireURL: {"mangadex": mangaDexURL}})
			stub := &stubConnector{key: "mangadex", resolved: map[string]*connectors.MangaResult{
				mangaDexURL: {Title: "Evolution Begins", URL: mangaDexURL, LatestChapter: tc.latest},
			}}
			var scanned connectors.Connector = stub
			if tc.trailing {
				scanned = &trailingConnector{stub}
			}
			store := newStubStore(mangaFireTarget(542))

			runScanOn(t, store, scanned, nil, mangaFire)

			stored := store.stored[1]
			if len(stored) != 1 || stored[0].Score != tc.wantScore {
				t.Fatalf("stored = %+v, want one candidate scored %v", stored, tc.wantScore)
			}
		})
	}
}

// When the scanned site publishes cross links, a hit linking the tracker's
// own page is the series even when the title ranked it lower, and a hit
// linking another page on a site the tracker is linked to is dropped.
func TestSearchHitsAreConfirmedByTheirLinks(t *testing.T) {
	const (
		wrongHit = "https://mangafire.to/title/aaa11-renge-to-naruto-colored"
		rightHit = "https://mangafire.to/title/bbb22-renge-to-naruto"
		vagueHit = "https://mangafire.to/title/ccc33-renge"
	)
	mangaFire := newMangaFire(map[string]map[string]string{
		wrongHit: {"mangadex": mangaDexOther},
		rightHit: {"mangadex": mangaDexURL, "mangaupdates": "https://mangaupdates.com/series/abc"},
	})
	mangaFire.searchResults = map[string][]connectors.MangaResult{"Renge to Naruto!": {
		{Title: "Renge to Naruto!", URL: wrongHit},
		{Title: "Renge", URL: vagueHit},
		{Title: "Renge to Naruto", URL: rightHit},
	}}
	mangaFire.resolved = map[string]*connectors.MangaResult{rightHit: {URL: rightHit, LatestChapter: chapter(64)}}
	mangaDex := &stubConnector{key: "mangadex"}
	store := newStubStore(repository.LinkScanTarget{
		TrackerID:          1,
		Title:              "Renge to Naruto!",
		LatestKnownChapter: chapter(64),
		Links:              []repository.TrackerLink{{SourceID: 1, SourceKey: "mangadex", URL: mangaDexURL}},
	})

	runScanOn(t, store, mangaFire, nil, mangaDex)

	stored := store.stored[1]
	if len(stored) != 1 || stored[0].CandidateURL != rightHit || stored[0].Score != 1.0 || stored[0].Basis != "mangafire" {
		t.Fatalf("stored = %+v, want only the hit that links the tracker's MangaDex page", stored)
	}
}

// Hits that say nothing about the tracker's sites stay as the title ranked
// them, and one linking another page on a tracker's site is still dropped.
func TestUnconfirmedHitsKeepTheirTitleScore(t *testing.T) {
	const (
		wrongHit = "https://mangafire.to/title/aaa11-mimi"
		otherHit = "https://mangafire.to/title/ddd44-mimi"
	)
	mangaFire := newMangaFire(map[string]map[string]string{
		wrongHit: {"mangadex": mangaDexOther},
		otherHit: {"mangaupdates": "https://mangaupdates.com/series/xyz"},
	})
	mangaFire.searchResults = map[string][]connectors.MangaResult{"Mimi": {
		{Title: "Mimi", URL: wrongHit},
		{Title: "Mimi", URL: otherHit},
	}}
	store := newStubStore(repository.LinkScanTarget{
		TrackerID: 1,
		Title:     "Mimi",
		Links:     []repository.TrackerLink{{SourceID: 1, SourceKey: "mangadex", URL: mangaDexURL}},
	})

	runScanOn(t, store, mangaFire, nil, &stubConnector{key: "mangadex"})

	stored := store.stored[1]
	if len(stored) != 1 || stored[0].CandidateURL != otherHit || stored[0].Basis != "" {
		t.Fatalf("stored = %+v, want only the hit that says nothing, unconfirmed", stored)
	}
}

// A record a linked site names is read by id. It still has to share a title
// with the tracker: an edition linked to the original's record must not take
// on the original's titles.
func TestAggregatorIsReadByIDWhenLinked(t *testing.T) {
	const manga = "https://www.mangaupdates.com/series/04gtb06"
	mangaFire := newMangaFire(map[string]map[string]string{mangaFireURL: {"mangabaka": "https://mangabaka.org/2996/"}})
	mangaUpdates := &trailingConnector{&stubConnector{key: "mangaupdates", resolved: map[string]*connectors.MangaResult{
		manga: {Title: "Tensei Shitara Slime datta Ken", URL: manga},
	}}}
	aid := &stubAid{
		byID:   map[int64]mangabaka.Series{2996: {ID: 2996, Type: "manga", Titles: []string{"That Time I Got Reincarnated as a Slime", "Tensei Shitara Slime datta Ken"}, MangaUpdatesID: "04gtb06"}},
		series: []mangabaka.Series{{ID: 85434, Type: "novel", Titles: []string{"That Time I Got Reincarnated as a Slime"}, MangaUpdatesID: "q5n1lxg"}},
	}
	target := mangaFireTarget(147)
	target.Title = "That Time I Got Reincarnated as a Slime"
	store := newStubStore(target)

	runScanOn(t, store, mangaUpdates, aid, mangaFire)

	if len(aid.idAsks) != 1 || aid.idAsks[0] != 2996 {
		t.Fatalf("id asks = %v, want the linked record 2996", aid.idAsks)
	}
	stored := store.stored[1]
	if len(stored) != 1 || stored[0].CandidateURL != manga {
		t.Fatalf("stored = %+v, want the manga's MangaUpdates entry", stored)
	}
	if titles := store.mergedTitles[1]; len(titles) == 0 || titles[len(titles)-1] != "Tensei Shitara Slime datta Ken" {
		t.Fatalf("merged titles = %v, want the linked record's", titles)
	}
}

func TestLinkedRecordSharingNoTitleIsIgnored(t *testing.T) {
	mangaFire := newMangaFire(map[string]map[string]string{mangaFireURL: {"mangabaka": "https://mangabaka.org/6957/"}})
	aid := &stubAid{byID: map[int64]mangabaka.Series{6957: {ID: 6957, Titles: []string{"Teasing Master Takagi-san"}, MangaUpdatesID: "3kqrqxe"}}}
	target := mangaFireTarget(179)
	target.Title = "Teasing Master Takagi-san (Official Colored)"
	store := newStubStore(target)

	runScanOn(t, store, &stubConnector{key: "comick"}, aid, mangaFire)

	if titles := store.mergedTitles[1]; len(titles) != 0 {
		t.Fatalf("merged %v; the original's titles must not reach the colored edition", titles)
	}
}

// The title search lists a light novel and its manga under one title. A manga
// tracker gets the manga's record, and when only the novel shares the title
// its MangaUpdates entry is not linked.
func TestTitleSearchPassesOverTheNovel(t *testing.T) {
	const (
		novel = "https://www.mangaupdates.com/series/q5n1lxg"
		manga = "https://www.mangaupdates.com/series/04gtb06"
	)
	novelRecord := mangabaka.Series{ID: 85434, Type: "novel", Titles: []string{"That Time I Got Reincarnated as a Slime"}, MangaUpdatesID: "q5n1lxg"}
	mangaRecord := mangabaka.Series{ID: 2996, Type: "manga", Titles: []string{"That Time I Got Reincarnated as a Slime"}, MangaUpdatesID: "04gtb06"}
	resolved := map[string]*connectors.MangaResult{
		novel: {Title: "Tensei Shitara Slime datta Ken (Novel)", URL: novel},
		manga: {Title: "Tensei Shitara Slime datta Ken", URL: manga},
	}

	for name, tc := range map[string]struct {
		records []mangabaka.Series
		links   []repository.TrackerLink
		want    string
	}{
		"manga tracker":              {records: []mangabaka.Series{novelRecord, mangaRecord}, want: manga},
		"only the novel matches":     {records: []mangabaka.Series{novelRecord}, want: ""},
		"a tracker that reads prose": {records: []mangabaka.Series{novelRecord, mangaRecord}, links: []repository.TrackerLink{{SourceKey: "freewebnovel", URL: "https://freewebnovel.com/novel/slime"}}, want: novel},
	} {
		t.Run(name, func(t *testing.T) {
			mangaUpdates := &trailingConnector{&stubConnector{key: "mangaupdates", resolved: resolved}}
			store := newStubStore(repository.LinkScanTarget{TrackerID: 1, Title: "That Time I Got Reincarnated as a Slime", Links: tc.links})

			runScanOn(t, store, mangaUpdates, &stubAid{series: tc.records})

			var urls []string
			for _, suggestion := range store.stored[1] {
				urls = append(urls, suggestion.CandidateURL)
			}
			if tc.want == "" {
				for _, url := range urls {
					if url == novel {
						t.Fatalf("stored %v; the novel's entry must not be linked to a manga tracker", urls)
					}
				}
				return
			}
			if len(urls) != 1 || urls[0] != tc.want {
				t.Fatalf("stored %v, want %s", urls, tc.want)
			}
		})
	}
}

// A linked tracker whose stored link another site's cross link contradicts is
// offered a replacement; one whose link agrees has any old offer withdrawn.
func TestReplacementPassFlagsContradictedLinks(t *testing.T) {
	const (
		novel    = "https://www.mangaupdates.com/series/q5n1lxg/slime-novel"
		manga    = "https://www.mangaupdates.com/series/04gtb06/slime"
		agreeMF  = "https://mangafire.to/title/eee55-agrees"
		silentMF = "https://mangafire.to/title/fff66-silent"
		deadMF   = "https://mangafire.to/title/ggg77-unreachable"
	)
	mangaFire := newMangaFire(map[string]map[string]string{
		mangaFireURL: {"mangaupdates": "https://mangaupdates.com/series/04gtb06/"},
		agreeMF:      {"mangaupdates": "https://www.mangaupdates.com/series/04gtb06/slime"},
		silentMF:     {"mangadex": mangaDexURL},
	})
	mangaUpdates := &trailingConnector{&stubConnector{key: "mangaupdates", resolved: map[string]*connectors.MangaResult{
		"https://mangaupdates.com/series/04gtb06/": {Title: "Tensei Shitara Slime datta Ken", URL: manga, SourceItemID: "4gtb06", LatestChapter: chapter(106)},
	}}}
	linked := func(id int64, mangaFirePage string, current string) repository.LinkScanTarget {
		return repository.LinkScanTarget{
			TrackerID:    id,
			Title:        "Slime",
			Links:        []repository.TrackerLink{{SourceKey: "mangafire", URL: mangaFirePage}, {SourceKey: "mangaupdates", URL: current}},
			CurrentLinks: []string{current},
		}
	}
	store := newStubStore()
	store.linked = []repository.LinkScanTarget{
		linked(1, mangaFireURL, novel),
		linked(2, agreeMF, manga),
		linked(3, silentMF, novel),
		linked(4, deadMF, novel),
	}
	store.replacements[2] = &repository.LinkSuggestion{CandidateURL: "stale offer"}
	store.replacements[4] = &repository.LinkSuggestion{CandidateURL: "kept while unknown"}

	scanner := runScanOn(t, store, mangaUpdates, nil, mangaFire)

	offer := store.replacements[1]
	if offer == nil || offer.CandidateURL != manga || offer.ReplacesURL == nil || *offer.ReplacesURL != novel || offer.Basis != "mangafire" {
		t.Fatalf("tracker 1 offer = %+v, want the manga replacing the novel", offer)
	}
	for _, id := range []int64{2, 3} {
		if offer, ok := store.replacements[id]; !ok || offer != nil {
			t.Fatalf("tracker %d offer = %+v, want withdrawn", id, offer)
		}
	}
	if offer := store.replacements[4]; offer == nil || offer.CandidateURL != "kept while unknown" {
		t.Fatalf("tracker 4 offer = %+v; a site that could not be asked must leave it as it was", offer)
	}
	progress := scanner.StatusFor(1).Progress
	if progress.Replacements != 1 || progress.Total != 4 || progress.Done != 4 {
		t.Fatalf("progress = %+v, want 1 replacement over 4 checked", progress)
	}
}

// A replacement the reader turned down — they kept their link — is neither
// offered again nor counted.
func TestKeptLinkIsNotOfferedAgain(t *testing.T) {
	const (
		current = "https://www.mangaupdates.com/series/kyx126d/the-lord-of-coins"
		oneShot = "https://www.mangaupdates.com/series/20v6jot/one-shot"
	)
	mangaFire := newMangaFire(map[string]map[string]string{mangaFireURL: {"mangaupdates": oneShot}})
	mangaUpdates := &trailingConnector{&stubConnector{key: "mangaupdates", resolved: map[string]*connectors.MangaResult{
		oneShot: {Title: "Does the Lord's Coin Not Shrink?!", URL: oneShot},
	}}}
	store := newStubStore()
	store.linked = []repository.LinkScanTarget{{
		TrackerID:    1,
		Title:        "The Lord of Coins",
		Links:        []repository.TrackerLink{{SourceKey: "mangafire", URL: mangaFireURL}, {SourceKey: "mangaupdates", URL: current}},
		CurrentLinks: []string{current},
	}}
	store.decided[1] = []string{oneShot}

	scanner := runScanOn(t, store, mangaUpdates, nil, mangaFire)

	if offer, ok := store.replacements[1]; !ok || offer != nil {
		t.Fatalf("offer = %+v, want none", offer)
	}
	if got := scanner.StatusFor(1).Progress.Replacements; got != 0 {
		t.Fatalf("replacements = %d, want 0", got)
	}
}

// A scan of a site no cross link can name does not walk the linked trackers.
func TestReplacementPassNeedsASiteThatCanBeNamed(t *testing.T) {
	store := newStubStore()
	store.linked = []repository.LinkScanTarget{{TrackerID: 1, Title: "x", CurrentLinks: []string{"https://comick.io/comic/x"}}}

	scanner := runScanOn(t, store, &stubConnector{key: "comick"}, nil, newMangaFire(nil))

	if got := scanner.StatusFor(1).Progress.Total; got != 0 {
		t.Fatalf("total = %d; ComicK is named by no cross link, so nothing linked is checked", got)
	}
}
