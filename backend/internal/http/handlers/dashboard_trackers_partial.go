package handlers

import (
	"context"
	"fmt"
	"math"
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
	siteLinks := buildTrackerSiteLinks(linkedSites, sourceLogoBySourceID)

	return h.render(c, "trackers_partial.html", trackersPartialData{
		Trackers:      cards,
		SiteLinks:     siteLinks,
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

		if item.LatestReleaseAt != nil {
			card.LatestReleaseFormatted = item.LatestReleaseAt.Format("2006-01-02 15:04")
			card.LatestReleaseAgo = relativeTime(*item.LatestReleaseAt)
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

		if item.LatestKnownChapter != nil {
			latestChapterURL, waitingLatestChapterURL := h.getCachedOrQueueChapterURL(sourceKey, item.SourceURL, *item.LatestKnownChapter, alternates, pageKey)
			card.LatestKnownChapterURL = latestChapterURL
			if waitingLatestChapterURL {
				pendingCovers = true
			}
		}

		if item.LastReadChapter != nil {
			lastReadChapterURL, waitingLastReadChapterURL := h.getCachedOrQueueChapterURL(sourceKey, item.SourceURL, *item.LastReadChapter, alternates, pageKey)
			card.LastReadChapterURL = lastReadChapterURL
			if waitingLastReadChapterURL {
				pendingCovers = true
			}
		}

		coverURL, servingSourceKey, waitingCover := h.getCachedOrQueueCover(sourceKey, item.SourceURL, item.SourceItemID, alternates, pageKey)
		card.CoverURL = coverURL
		if waitingCover {
			pendingCovers = true
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

func buildTrackerSiteLinks(sources []models.Source, sourceLogoBySourceID map[int64]string) []trackerSiteLinkView {
	links := make([]trackerSiteLinkView, 0, len(sources))
	for _, source := range sources {
		homeURL := sourceHomeURLForKey(source.Key)
		if source.BaseURL != nil && strings.TrimSpace(*source.BaseURL) != "" {
			homeURL = strings.TrimSpace(*source.BaseURL)
		}
		if homeURL == "" {
			continue
		}

		links = append(links, trackerSiteLinkView{
			Name:    source.Name,
			HomeURL: homeURL,
			LogoURL: strings.TrimSpace(sourceLogoBySourceID[source.ID]),
		})
	}

	return links
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
		h.setCachedCover(cacheKey, "", false, 2*time.Minute)
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
func (h *DashboardHandler) getCachedOrQueueChapterURL(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, pageKey string) (string, bool) {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return "", false
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL, false
	}

	cacheKey := buildChapterURLCacheKey(trimmedSourceKey, trimmedSourceURL, chapter)
	if cachedChapterURL, found, ok := h.getCachedChapterURL(cacheKey); ok {
		if found {
			return cachedChapterURL, false
		}
		return trimmedSourceURL, false
	}

	h.queueChapterURLResolve(trimmedSourceKey, trimmedSourceURL, chapter, alternates, cacheKey, pageKey)
	return trimmedSourceURL, true
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

// fetchChapterURL resolves a chapter's reader URL from a tracker's primary source,
// falling back to its alternate linked sources when the primary cannot answer, so
// a blocked site does not leave every chapter link pointing nowhere useful.
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

	// Primary source first, then the tracker's other linked sources.
	candidates := make([]repository.TrackerSourceRef, 0, len(alternates)+1)
	candidates = append(candidates, repository.TrackerSourceRef{SourceKey: trimmedSourceKey, SourceURL: trimmedSourceURL})
	candidates = append(candidates, alternates...)

	// A source that was actually queried and failed may succeed shortly; one with
	// no usable connector will not, so the two are cached for different spans.
	attempted := false
	var lastErr error

	for _, candidate := range candidates {
		candidateKey := strings.TrimSpace(candidate.SourceKey)
		candidateURL := strings.TrimSpace(candidate.SourceURL)
		if candidateKey == "" || candidateURL == "" {
			continue
		}

		connector, ok := h.registry.Get(candidateKey)
		if !ok {
			lastErr = fmt.Errorf("connector not found")
			continue
		}

		resolver, ok := connector.(connectors.ChapterURLResolver)
		if !ok {
			lastErr = fmt.Errorf("chapter resolver not supported")
			continue
		}

		attempted = true
		chapterURL, err := h.resolveChapterURLFromConnector(resolver, candidateURL, chapter)
		if err != nil {
			lastErr = err
			continue
		}
		if chapterURL == "" {
			lastErr = fmt.Errorf("chapter url empty")
			continue
		}

		h.setCachedChapterURL(cacheKey, chapterURL, true, 12*time.Hour)
		return chapterURL, nil
	}

	negativeTTL := 30 * time.Minute
	if attempted {
		negativeTTL = 2 * time.Minute
	}
	h.setCachedChapterURL(cacheKey, "", false, negativeTTL)
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable source")
	}
	return trimmedSourceURL, fmt.Errorf("resolve chapter url: %w", lastErr)
}

func (h *DashboardHandler) resolveChapterURLFromConnector(resolver connectors.ChapterURLResolver, sourceURL string, chapter float64) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
