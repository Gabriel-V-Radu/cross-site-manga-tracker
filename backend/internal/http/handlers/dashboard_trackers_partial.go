package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

func (h *DashboardHandler) TrackersPartial(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	c.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Set("Pragma", "no-cache")
	c.Set("Expires", "0")

	status := strings.TrimSpace(c.Query("status", "reading"))
	statuses := make([]string, 0)
	if status != "" && status != "all" {
		statuses = append(statuses, status)
	}

	viewMode := normalizeViewMode(c.Query("view", "grid"))
	page := parsePositiveInt(c.Query("page", "1"), 1)
	const pageSize = 24

	listOptions := repository.TrackerListOptions{
		ProfileID: activeProfile.ID,
		Statuses:  statuses,
		TagNames:  parseTagNamesFromQuery(c),
		SourceIDs: parseSourceIDsFromQuery(c),
		SortBy:    strings.TrimSpace(c.Query("sort", "latest_known_chapter")),
		Order:     strings.TrimSpace(c.Query("order", "desc")),
		Query:     strings.TrimSpace(c.Query("q")),
	}

	totalTrackers, err := h.trackerRepo.Count(listOptions)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load trackers")
	}

	totalPages := int(math.Ceil(float64(totalTrackers) / float64(pageSize)))
	if totalPages < 1 {
		totalPages = 1
	}

	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize
	listOptions.Limit = pageSize
	listOptions.Offset = offset
	refreshKey := c.OriginalURL()
	h.setActiveTrackersPageKey(refreshKey)

	items, err := h.trackerRepo.List(listOptions)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load trackers")
	}

	hasNextPage := page < totalPages
	linkedSites, err := h.listLinkedSourcesForProfile(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sites")
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked site logos")
	}

	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}

	sourceByID := make(map[int64]models.Source, len(sources))
	for _, source := range sources {
		sourceByID[source.ID] = source
	}

	// Covers and chapter links resolve against a tracker's primary source; these
	// let them fall back when that source is unreadable.
	alternatesByTracker, err := h.trackerRepo.ListAlternateSourcesByTracker(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sources")
	}

	cards, pendingCovers := h.buildTrackerCards(items, sourceByID, sourceLogoBySourceID, alternatesByTracker, refreshKey)

	// The connector shortcuts rank by how many trackers each source serves:
	// with ten registered sources the row would otherwise wrap into a second
	// line, so only the top few show and the rest fold behind a toggle.
	trackerCountsBySource, err := h.trackerRepo.CountTrackersBySource(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to count linked sites")
	}
	topSiteLinks, moreSiteLinks := buildTrackerSiteLinks(linkedSites, sourceLogoBySourceID, trackerCountsBySource)

	return h.render(c, "trackers_partial.html", trackersPartialData{
		Trackers:      cards,
		SiteLinks:     topSiteLinks,
		MoreSiteLinks: moreSiteLinks,
		ViewMode:      viewMode,
		Page:          page,
		PrevPage:      max(1, page-1),
		NextPage:      min(totalPages, page+1),
		TotalResults:  totalTrackers,
		TotalPages:    totalPages,
		PageNumbers:   buildPageNumbers(totalPages, page),
		HasPrevPage:   page > 1,
		HasNextPage:   hasNextPage,
		PendingCovers: pendingCovers,
		RefreshKey:    refreshKey,
	})
}

func normalizeViewMode(raw string) string {
	viewMode := strings.TrimSpace(raw)
	if viewMode != "grid" && viewMode != "list" {
		return "grid"
	}
	return viewMode
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func buildPageNumbers(totalPages int, currentPage int) []int {
	if totalPages <= 0 {
		return []int{1}
	}
	if currentPage <= 0 {
		currentPage = 1
	}
	if currentPage > totalPages {
		currentPage = totalPages
	}

	if totalPages <= 11 {
		pages := make([]int, totalPages)
		for idx := 0; idx < totalPages; idx++ {
			pages[idx] = idx + 1
		}
		return pages
	}

	pages := make([]int, 0, 9)
	pages = append(pages, 1)

	windowStart := currentPage - 2
	windowEnd := currentPage + 2

	if windowStart < 2 {
		windowStart = 2
	}
	if windowEnd > totalPages-1 {
		windowEnd = totalPages - 1
	}

	if windowStart > 2 {
		pages = append(pages, 0)
	}

	for page := windowStart; page <= windowEnd; page++ {
		pages = append(pages, page)
	}

	if windowEnd < totalPages-1 {
		pages = append(pages, 0)
	}

	pages = append(pages, totalPages)

	return pages
}

func (h *DashboardHandler) buildTrackerCards(items []models.Tracker, sourceByID map[int64]models.Source, sourceLogoBySourceID map[int64]string, alternatesByTracker map[int64][]repository.TrackerSourceRef, pageKey string) ([]trackerCardView, bool) {
	cards := make([]trackerCardView, 0, len(items))
	sourceNameByKey := buildSourceNameByKey(sourceByID)
	pendingCovers := false
	for _, item := range items {
		tagViews := toTrackerTagView(item.Tags)
		displayTags, hiddenTagCount := prioritizeTrackerTags(tagViews, 3)

		card := trackerCardView{
			ID:                     item.ID,
			Title:                  item.Title,
			Status:                 item.Status,
			StatusLabel:            statusLabel(item.Status),
			Tags:                   displayTags,
			HiddenTagCount:         hiddenTagCount,
			TagIcons:               toTrackerTagIcons(item.Tags),
			SourceURL:              item.SourceURL,
			LatestKnownChapterURL:  item.SourceURL,
			LastReadChapterURL:     item.SourceURL,
			SourceItemID:           item.SourceItemID,
			Rating:                 item.Rating,
			LatestKnownChapterRaw:  item.LatestKnownChapter,
			LastReadChapterRaw:     item.LastReadChapter,
			LatestReleaseAgo:       "—",
			LatestReleaseFormatted: "—",
			UpdatedAtFormatted:     item.UpdatedAt.Format("2006-01-02 15:04"),
			LastReadAgo:            "—",
		}

		if item.LastReadAt != nil {
			card.LastReadAgo = relativeTime(*item.LastReadAt)
		}

		if item.LastCheckedAt != nil {
			card.LastCheckedFormatted = item.LastCheckedAt.Format("2006-01-02 15:04")
			card.LastCheckedAgo = relativeTime(*item.LastCheckedAt)
		} else {
			card.LastCheckedFormatted = "—"
			card.LastCheckedAgo = "—"
		}

		// A source that reports a chapter number without a date used to leave the
		// card showing "—", which said nothing about how old the chapter was and
		// left the user no way to tell a finished series from one that updated
		// today. When no date was ever reported, the card falls back to when this
		// app first saw the chapter and marks the value as approximate.
		if item.LatestReleaseAt != nil {
			card.LatestReleaseFormatted = item.LatestReleaseAt.Format("2006-01-02 15:04")
			card.LatestReleaseAgo = relativeTime(*item.LatestReleaseAt)
		} else if item.LatestChapterSeenAt != nil {
			card.LatestReleaseFormatted = item.LatestChapterSeenAt.Format("2006-01-02 15:04")
			card.LatestReleaseAgo = "~" + relativeTime(*item.LatestChapterSeenAt)
			card.LatestReleaseApproximate = true
			card.LatestReleaseTitle = "The site reports no release date. First seen here on " +
				card.LatestReleaseFormatted + " UTC."
		}

		if item.LatestKnownChapter != nil {
			card.LatestKnownChapter = formatChapterLabel(*item.LatestKnownChapter)
		} else {
			card.LatestKnownChapter = "—"
		}

		if item.LastReadChapter != nil {
			card.LastReadChapter = formatChapterLabel(*item.LastReadChapter)
		} else {
			card.LastReadChapter = "—"
		}

		if item.Rating != nil {
			card.RatingLabel = formatRatingLabel(*item.Rating)
		}

		source := sourceByID[item.SourceID]
		sourceKey := strings.TrimSpace(source.Key)
		sourceName := strings.TrimSpace(source.Name)
		if sourceName == "" {
			if sourceKey != "" {
				sourceName = humanizeValueLabel(sourceKey)
			} else {
				sourceName = "Site"
			}
		}

		card.SourceLogoURL = strings.TrimSpace(sourceLogoBySourceID[item.SourceID])
		card.SourceLogoLabel = sourceName

		alternates := alternatesByTracker[item.ID]

		// The site holding the newest chapter is where the user goes to read, so
		// it decides which source the card presents. The cover used to decide it,
		// which is the weakest signal on the card: art resolves from a different
		// endpoint than chapters do, so a card could badge one site while its
		// chapter links opened another.
		latestChapterSourceKey := ""

		// A pinned reading source narrows the chapter links to that one site:
		// its resolved chapter page, its offline-built reader URL, or at worst
		// its series page — never another site. Auto keeps the full chain.
		pinned := pinnedReadingRef(item, sourceKey, alternates)
		chapterKey, chapterBaseURL, chapterAlternates := sourceKey, item.SourceURL, alternates
		if pinned != nil {
			chapterKey, chapterBaseURL, chapterAlternates = pinned.SourceKey, pinned.SourceURL, nil
			card.LatestKnownChapterURL = pinned.SourceURL
			card.LastReadChapterURL = pinned.SourceURL
		}
		card.HighlightURL = chapterBaseURL

		if item.LatestKnownChapter != nil {
			latestChapterURL, resolvedLatest, waitingLatestChapterURL := h.getCachedOrQueueChapterURL(chapterKey, chapterBaseURL, *item.LatestKnownChapter, chapterAlternates, pageKey)
			card.LatestKnownChapterURL = latestChapterURL
			if resolvedLatest {
				latestChapterSourceKey = inferSourceKeyFromURL(latestChapterURL)
				card.LatestKnownChapterSite = chapterSiteLabel(latestChapterSourceKey, sourceNameByKey)
			}
			if waitingLatestChapterURL {
				pendingCovers = true
			}
		}

		// Under auto, point the open-to-read button at the same site the
		// latest-chapter link lands on: a MangaUpdates-primary tracker whose
		// chapters open on MangaFire should open its series there too.
		if pinned == nil && latestChapterSourceKey != "" && latestChapterSourceKey != sourceKey {
			for _, alternate := range alternates {
				if strings.EqualFold(strings.TrimSpace(alternate.SourceKey), latestChapterSourceKey) &&
					strings.TrimSpace(alternate.SourceURL) != "" {
					card.HighlightURL = alternate.SourceURL
					break
				}
			}
		}

		if item.LastReadChapter != nil {
			lastReadChapterURL, resolvedLastRead, waitingLastReadChapterURL := h.getCachedOrQueueChapterURL(chapterKey, chapterBaseURL, *item.LastReadChapter, chapterAlternates, pageKey)
			card.LastReadChapterURL = lastReadChapterURL
			if resolvedLastRead {
				card.LastReadChapterSite = chapterSiteLabel(inferSourceKeyFromURL(lastReadChapterURL), sourceNameByKey)
			}
			if waitingLastReadChapterURL {
				pendingCovers = true
			}
		}

		coverURL, coverSourceKey, waitingCover := h.getCachedOrQueueCover(sourceKey, item.SourceURL, item.SourceItemID, alternates, pageKey)
		card.CoverURL = coverURL
		if waitingCover {
			pendingCovers = true
		}

		// Only fall back to the cover's source when no chapter link resolved:
		// otherwise a card whose primary site is down would still badge that site
		// while every working link pointed at the mirror.
		servingSourceKey := latestChapterSourceKey
		if servingSourceKey == "" {
			servingSourceKey = coverSourceKey
		}

		// When a fallback source served this card, present that source rather than
		// the primary: a badge naming a site that supplied nothing, next to links
		// pointing somewhere else, is worse than no badge at all.
		if serving, ok := findServingSource(servingSourceKey, sourceKey, alternates); ok {
			card.SourceURL = serving.SourceURL
			card.SourceLogoURL = strings.TrimSpace(sourceLogoBySourceID[serving.SourceID])
			if servingSource, found := sourceByID[serving.SourceID]; found {
				card.SourceLogoLabel = servingSource.Name
			}
		}

		cards = append(cards, card)
	}

	return cards, pendingCovers
}

// buildSourceNameByKey indexes the enabled sources by their key, so a link whose
// host has been resolved to a source key can be labelled with the site's own
// display name rather than the key.
func buildSourceNameByKey(sourceByID map[int64]models.Source) map[string]string {
	names := make(map[string]string, len(sourceByID))
	for _, source := range sourceByID {
		key := strings.ToLower(strings.TrimSpace(source.Key))
		name := strings.TrimSpace(source.Name)
		if key == "" || name == "" {
			continue
		}
		names[key] = name
	}
	return names
}

// chapterSiteLabel names the site a chapter link opens. An unknown host yields
// no label rather than a guess: a link that says nothing is better than one that
// names the wrong site.
func chapterSiteLabel(sourceKey string, sourceNameByKey map[string]string) string {
	key := strings.ToLower(strings.TrimSpace(sourceKey))
	if key == "" {
		return ""
	}
	if name := sourceNameByKey[key]; name != "" {
		return name
	}
	return humanizeValueLabel(key)
}

// findServingSource resolves the source key that supplied a card's data to the
// matching alternate. It reports false when the primary source served the card,
// when nothing has resolved yet, or when the key names no linked alternate —
// all cases where the card should keep presenting its primary source.
func findServingSource(servingSourceKey, primarySourceKey string, alternates []repository.TrackerSourceRef) (repository.TrackerSourceRef, bool) {
	serving := strings.TrimSpace(servingSourceKey)
	if serving == "" || strings.EqualFold(serving, strings.TrimSpace(primarySourceKey)) {
		return repository.TrackerSourceRef{}, false
	}

	for _, alternate := range alternates {
		if !strings.EqualFold(strings.TrimSpace(alternate.SourceKey), serving) {
			continue
		}
		if strings.TrimSpace(alternate.SourceURL) == "" || alternate.SourceID <= 0 {
			continue
		}
		return alternate, true
	}

	return repository.TrackerSourceRef{}, false
}

// pinnedReadingRef returns the linked source the tracker's reading links are
// pinned to, or nil for auto. A stale pin — the source was unlinked since —
// degrades to auto rather than pointing links at a site the tracker no longer
// carries.
func pinnedReadingRef(item models.Tracker, primaryKey string, alternates []repository.TrackerSourceRef) *repository.TrackerSourceRef {
	if item.ReadingSourceID == nil {
		return nil
	}
	if *item.ReadingSourceID == item.SourceID {
		return &repository.TrackerSourceRef{
			SourceID:     item.SourceID,
			SourceKey:    primaryKey,
			SourceItemID: item.SourceItemID,
			SourceURL:    item.SourceURL,
		}
	}
	for index := range alternates {
		if alternates[index].SourceID == *item.ReadingSourceID {
			return &alternates[index]
		}
	}
	return nil
}

// maxVisibleSiteLinks is how many connector shortcuts show before the rest
// fold behind the "+N" toggle.
const maxVisibleSiteLinks = 4

func buildTrackerSiteLinks(sources []models.Source, sourceLogoBySourceID map[int64]string, trackerCounts map[int64]int) ([]trackerSiteLinkView, []trackerSiteLinkView) {
	type rankedLink struct {
		view  trackerSiteLinkView
		count int
	}

	ranked := make([]rankedLink, 0, len(sources))
	for _, source := range sources {
		homeURL := sourceHomeURLForKey(source.Key)
		if source.BaseURL != nil && strings.TrimSpace(*source.BaseURL) != "" {
			homeURL = strings.TrimSpace(*source.BaseURL)
		}
		if homeURL == "" {
			continue
		}

		ranked = append(ranked, rankedLink{
			view: trackerSiteLinkView{
				Name:    source.Name,
				HomeURL: homeURL,
				LogoURL: strings.TrimSpace(sourceLogoBySourceID[source.ID]),
			},
			count: trackerCounts[source.ID],
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].view.Name < ranked[j].view.Name
	})

	links := make([]trackerSiteLinkView, 0, len(ranked))
	for _, item := range ranked {
		links = append(links, item.view)
	}
	if len(links) <= maxVisibleSiteLinks {
		return links, nil
	}
	return links[:maxVisibleSiteLinks], links[maxVisibleSiteLinks:]
}

// getCachedOrQueueCover returns the cover URL, the source key that supplied it,
// and whether a background fetch is still pending. The serving source is empty
// until a fetch completes, so a first render shows the tracker's primary source
// and a later one corrects it if a fallback answered instead.
func (h *DashboardHandler) getCachedOrQueueCover(sourceKey, sourceURL string, sourceItemID *string, alternates []repository.TrackerSourceRef, pageKey string) (string, string, bool) {
	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return "", "", false
	}

	cacheKey := buildCoverCacheKey(trimmedSourceKey, sourceURL, sourceItemID)
	if cachedURL, servingKey, found, ok := h.getCachedCoverWithSource(cacheKey); ok {
		if found {
			return cachedURL, servingKey, false
		}
		return "", "", false
	}

	if strings.TrimSpace(sourceURL) == "" {
		h.setCachedCover(cacheKey, "", false, jitteredTTL(lookupUnreachableTTL))
		return "", "", false
	}

	h.queueCoverFetch(trimmedSourceKey, sourceURL, sourceItemID, alternates, cacheKey, pageKey)
	return "", "", true
}

func (h *DashboardHandler) queueCoverFetch(sourceKey, sourceURL string, sourceItemID *string, alternates []repository.TrackerSourceRef, cacheKey string, pageKey string) {
	h.coverFetchMu.Lock()
	if h.coverInFlight[cacheKey] {
		h.coverFetchMu.Unlock()
		return
	}
	h.coverInFlight[cacheKey] = true
	h.coverFetchMu.Unlock()

	go func() {
		isMangafire := strings.EqualFold(strings.TrimSpace(sourceKey), "mangafire")
		if isMangafire {
			h.mangafireCoverSem <- struct{}{}
		} else {
			h.coverFetchSem <- struct{}{}
		}
		defer func() {
			if isMangafire {
				<-h.mangafireCoverSem
			} else {
				<-h.coverFetchSem
			}
			h.coverFetchMu.Lock()
			delete(h.coverInFlight, cacheKey)
			h.coverFetchMu.Unlock()
		}()

		if pageKey != "" && !h.isActiveTrackersPageKey(pageKey) {
			return
		}

		_, _ = h.fetchCoverURL(context.Background(), sourceKey, sourceURL, sourceItemID, alternates)
	}()
}

// getCachedOrQueueChapterURL returns a chapter's reader URL, whether that URL is
// a resolved chapter link rather than the series page it degrades to, and whether
// a background resolve is still pending. The caller needs the middle value to tell
// "this link opens chapter 65 on some site" from "we gave up and pointed at the
// series page", because only the former says which site is serving the card.
func (h *DashboardHandler) getCachedOrQueueChapterURL(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, pageKey string) (chapterURL string, resolved bool, waiting bool) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", false, false
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, false, false
	}

	cacheKey := buildChapterURLCacheKey(trimmedSourceKey, trimmedSourceURL, chapter)
	if cachedChapterURL, found, ok := h.getCachedChapterURL(cacheKey); ok {
		if found {
			return cachedChapterURL, true, false
		}
		return trimmedSourceURL, false, false
	}

	h.queueChapterURLResolve(trimmedSourceKey, trimmedSourceURL, chapter, alternates, cacheKey, pageKey)
	return trimmedSourceURL, false, true
}

func (h *DashboardHandler) queueChapterURLResolve(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, cacheKey string, pageKey string) {
	h.chapterURLFetchMu.Lock()
	if h.chapterURLInFlight[cacheKey] {
		h.chapterURLFetchMu.Unlock()
		return
	}
	h.chapterURLInFlight[cacheKey] = true
	h.chapterURLFetchMu.Unlock()

	go func() {
		h.chapterURLFetchSem <- struct{}{}
		defer func() {
			<-h.chapterURLFetchSem
			h.chapterURLFetchMu.Lock()
			delete(h.chapterURLInFlight, cacheKey)
			h.chapterURLFetchMu.Unlock()
		}()

		if pageKey != "" && !h.isActiveTrackersPageKey(pageKey) {
			return
		}

		_, _ = h.fetchChapterURL(sourceKey, sourceURL, chapter, alternates)
	}()
}

func (h *DashboardHandler) setActiveTrackersPageKey(pageKey string) {
	h.activePageMu.Lock()
	h.activePageKey = strings.TrimSpace(pageKey)
	h.activePageMu.Unlock()
}

func (h *DashboardHandler) isActiveTrackersPageKey(pageKey string) bool {
	h.activePageMu.RLock()
	activePage := h.activePageKey
	h.activePageMu.RUnlock()
	return strings.TrimSpace(pageKey) != "" && strings.TrimSpace(pageKey) == strings.TrimSpace(activePage)
}

// readerCandidateRank orders a tracker's linked sources for chapter-link
// resolution when no reading pin narrows the choice. Origin scanlator sites
// go first: for their own series they are where chapters appear before any
// aggregator mirrors them, and their readers are the best of the chain. Then
// MangaHub, English-only fresh, then the remaining reader sites in their
// incoming order. ComicK ranks last as the info floor: it always has the
// chapter page, which makes it the reliable fallback, but its reader is the
// worst of the chain — see fetchChapterURL for how the floor is only reached
// after every readable site and every offline-built link had its turn.
func readerCandidateRank(sourceKey string) int {
	switch strings.ToLower(strings.TrimSpace(sourceKey)) {
	case "asuracomic", "flamecomics":
		return 0
	case "mangahub":
		return 1
	case "comick":
		return readerRankInfoFloor
	default:
		return 2
	}
}

// readerRankInfoFloor marks sources nobody wants to read on: they take part
// in the chain only after every readable site and offline-built link failed.
const readerRankInfoFloor = 3

// orderReaderCandidates reorders candidates in place by readerCandidateRank,
// keeping the incoming order (primary first, then linked alternates) between
// sources of equal rank.
func orderReaderCandidates(candidates []repository.TrackerSourceRef) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return readerCandidateRank(candidates[i].SourceKey) < readerCandidateRank(candidates[j].SourceKey)
	})
}

// fetchChapterURL resolves a chapter's reader URL across a tracker's linked
// sources in three tiers, so a blocked site does not leave every chapter link
// pointing nowhere useful. First, every readable site in reading-priority
// order (see orderReaderCandidates) gets to verify it carries the chapter —
// a resolver that answers ErrChapterNotFound has answered, and cedes its
// turn for this chapter. Second, a site that could not be asked at all but
// can construct its reader URL offline (MangaFire behind its challenge)
// serves the built link — the reader's own browser passes the challenge the
// server cannot, so an unverified link there beats a verified one on the
// floor. Third, the info-floor sites (ComicK): they always carry the chapter
// page, but nobody wants to read there, so they only serve when nothing else
// could.
func (h *DashboardHandler) fetchChapterURL(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef) (string, error) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", fmt.Errorf("missing source url")
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, nil
	}

	cacheKey := buildChapterURLCacheKey(trimmedSourceKey, trimmedSourceURL, chapter)
	if cachedChapterURL, found, ok := h.getCachedChapterURL(cacheKey); ok {
		if found {
			return cachedChapterURL, nil
		}
		return trimmedSourceURL, fmt.Errorf("chapter url not found")
	}

	// Primary source first, then the tracker's other linked sources, reordered
	// by reading priority.
	candidates := make([]repository.TrackerSourceRef, 0, len(alternates)+1)
	candidates = append(candidates, repository.TrackerSourceRef{SourceKey: trimmedSourceKey, SourceURL: trimmedSourceURL})
	candidates = append(candidates, alternates...)
	orderReaderCandidates(candidates)

	// A source that was actually queried and failed may succeed shortly; one with
	// no usable connector will not, so the two are cached for different spans.
	attempted := false
	var lastErr error

	// Sites whose resolver could not be asked (as opposed to answering "not
	// carried") and that can build their reader URL offline, in chain order.
	type blockedLinkable struct {
		linker connectors.OfflineChapterLinker
		url    string
	}
	blocked := make([]blockedLinkable, 0, 1)
	infoFloor := make([]repository.TrackerSourceRef, 0, 1)

	resolveCandidate := func(candidateKey, candidateURL string) (string, bool) {
		connector, ok := h.registry.Get(candidateKey)
		if !ok {
			lastErr = fmt.Errorf("connector not found")
			return "", false
		}

		resolver, ok := connector.(connectors.ChapterURLResolver)
		if !ok {
			lastErr = fmt.Errorf("chapter resolver not supported")
			return "", false
		}

		attempted = true
		chapterURL, err := h.resolveChapterURLFromConnector(resolver, candidateURL, chapter)
		switch {
		case err != nil:
			lastErr = err
			// The site answered "I do not carry this chapter": its turn is
			// over, a built link would point at a page known not to exist.
			// Any other failure means the site never answered, so its
			// offline-built link may still claim a turn in the second tier.
			if !errors.Is(err, connectors.ErrChapterNotFound) {
				if linker, ok := connector.(connectors.OfflineChapterLinker); ok {
					blocked = append(blocked, blockedLinkable{linker: linker, url: candidateURL})
				}
			}
		case chapterURL == "":
			lastErr = fmt.Errorf("chapter url empty")
		default:
			return chapterURL, true
		}
		return "", false
	}

	// Tier 1: readable sites that verify they carry the chapter.
	for _, candidate := range candidates {
		candidateKey := strings.TrimSpace(candidate.SourceKey)
		candidateURL := strings.TrimSpace(candidate.SourceURL)
		if candidateKey == "" || candidateURL == "" {
			continue
		}
		if readerCandidateRank(candidateKey) == readerRankInfoFloor {
			infoFloor = append(infoFloor, candidate)
			continue
		}

		if chapterURL, ok := resolveCandidate(candidateKey, candidateURL); ok {
			h.setCachedChapterURL(cacheKey, chapterURL, true, 12*time.Hour)
			return chapterURL, nil
		}
	}

	// Tier 2: offline-built links from sites that could not be asked. Cached
	// for the retry span, not the full 12 hours: once the site answers the
	// server again, a verified resolution should replace the guess soon
	// rather than a day later.
	for _, candidate := range blocked {
		if built, buildOK := candidate.linker.BuildChapterURL(candidate.url, chapter); buildOK && strings.TrimSpace(built) != "" {
			h.setCachedChapterURL(cacheKey, built, true, jitteredTTL(lookupRetryTTL))
			return built, nil
		}
	}

	// Tier 3: the info floor — typically the site that reported the chapter
	// number in the first place, so at least its chapter page exists.
	for _, candidate := range infoFloor {
		candidateKey := strings.TrimSpace(candidate.SourceKey)
		candidateURL := strings.TrimSpace(candidate.SourceURL)
		if candidateKey == "" || candidateURL == "" {
			continue
		}
		if chapterURL, ok := resolveCandidate(candidateKey, candidateURL); ok {
			h.setCachedChapterURL(cacheKey, chapterURL, true, 12*time.Hour)
			return chapterURL, nil
		}
	}

	negativeTTL := lookupUnreachableTTL
	if attempted {
		negativeTTL = lookupRetryTTL
	}
	h.setCachedChapterURL(cacheKey, "", false, jitteredTTL(negativeTTL))
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable source")
	}
	return trimmedSourceURL, fmt.Errorf("resolve chapter url: %w", lastErr)
}

func (h *DashboardHandler) resolveChapterURLFromConnector(resolver connectors.ChapterURLResolver, sourceURL string, chapter float64) (string, error) {
	// Background work behind the shared per-host throttle: see the cover
	// resolve timeout for why this is generous.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	chapterURL, err := resolver.ResolveChapterURL(ctx, sourceURL, chapter)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(chapterURL), nil
}

func buildChapterURLCacheKey(sourceKey, sourceURL string, chapter float64) string {
	return strings.ToLower(strings.TrimSpace(sourceKey)) + "|" + strings.ToLower(strings.TrimSpace(sourceURL)) + "|" + strconv.FormatFloat(chapter, 'f', -1, 64)
}

func (h *DashboardHandler) getCachedChapterURL(cacheKey string) (chapterURL string, found bool, ok bool) {
	h.chapterURLCacheMu.RLock()
	entry, exists := h.chapterURLCache[cacheKey]
	h.chapterURLCacheMu.RUnlock()
	if !exists {
		return "", false, false
	}

	if time.Now().UTC().After(entry.ExpiresAt) {
		h.chapterURLCacheMu.Lock()
		delete(h.chapterURLCache, cacheKey)
		h.chapterURLCacheMu.Unlock()
		return "", false, false
	}

	return entry.ChapterURL, entry.Found, true
}

func (h *DashboardHandler) setCachedChapterURL(cacheKey, chapterURL string, found bool, ttl time.Duration) {
	h.chapterURLCacheMu.Lock()
	h.chapterURLCache[cacheKey] = chapterURLCacheEntry{
		ChapterURL: chapterURL,
		Found:      found,
		ExpiresAt:  time.Now().UTC().Add(ttl),
	}
	h.chapterURLCacheMu.Unlock()
}
