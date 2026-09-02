package handlers

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

func (h *DashboardHandler) TrackersPartial(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
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

	totalTrackers, err := h.trackerRepo.Count(c.Context(), listOptions)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load trackers", err)
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
	// Cloned because fiber hands back a string aliasing fasthttp's request-URI
	// buffer, which is overwritten by the next request on the same connection.
	// This key outlives the request twice over — it is held on the page gate for
	// the process's lifetime and captured by every background fetch queued below
	// — so without the copy a queued cover would wake up holding the bytes of
	// some later request, decide the reader had navigated away, and quietly do
	// nothing.
	refreshKey := strings.Clone(c.OriginalURL())
	h.pageKeys.SetActive(refreshKey)

	items, err := h.trackerRepo.List(c.Context(), listOptions)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load trackers", err)
	}

	hasNextPage := page < totalPages
	linkedSites, err := h.listLinkedSites(c.Context())
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load linked sites", err)
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListSourceLogoURLs(c.Context())
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load linked site logos", err)
	}

	sources, err := h.sourceRepo.ListEnabled(c.Context())
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load sources", err)
	}

	sourceByID := make(map[int64]models.Source, len(sources))
	for _, source := range sources {
		sourceByID[source.ID] = source
	}

	// Covers and chapter links resolve against a tracker's primary source; these
	// let them fall back when that source is unreadable.
	alternatesByTracker, err := h.trackerRepo.ListAlternateSourcesByTracker(c.Context(), activeProfile.ID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load linked sources", err)
	}

	cards, pendingCovers := h.buildTrackerCards(items, sourceByID, sourceLogoBySourceID, alternatesByTracker, refreshKey)

	// The connector shortcuts rank by how many trackers each source serves:
	// with ten registered sources the row would otherwise wrap into a second
	// line, so only the top few show and the rest fold behind a toggle.
	trackerCountsBySource, err := h.trackerRepo.CountTrackersBySource(c.Context(), activeProfile.ID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to count linked sites", err)
	}
	topSiteLinks, moreSiteLinks := buildTrackerSiteLinks(linkedSites, sourceLogoBySourceID, trackerCountsBySource, h.sourceHomeURLForKey)

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
		card, waiting := h.buildTrackerCard(item, sourceByID, sourceLogoBySourceID, sourceNameByKey, alternatesByTracker[item.ID], pageKey)
		if waiting {
			pendingCovers = true
		}
		cards = append(cards, card)
	}

	return cards, pendingCovers
}

// buildTrackerCard maps one tracker onto its card and runs the lookups the card
// depends on. Where the links and the badge end up pointing is not decided here:
// that is decideTrackerLinks, kept free of the handler so the precedence can be
// exercised without a request. The second return says a lookup is still running,
// which is what keeps the page asking for a corrected render.
func (h *DashboardHandler) buildTrackerCard(item models.Tracker, sourceByID map[int64]models.Source, sourceLogoBySourceID map[int64]string, sourceNameByKey map[string]string, alternates []repository.TrackerSourceRef, pageKey string) (trackerCardView, bool) {
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

	waiting := false

	// A pinned reading source narrows the chapter links to that one site:
	// its resolved chapter page, its offline-built reader URL, or at worst
	// its series page — never another site. Auto keeps the full chain.
	pinned := pinnedReadingRef(item, sourceKey, alternates)
	chapterKey, chapterBaseURL, chapterAlternates := sourceKey, item.SourceURL, alternates
	if pinned != nil {
		chapterKey, chapterBaseURL, chapterAlternates = pinned.SourceKey, pinned.SourceURL, nil
	}

	latestChapter := chapterLinkLookup{Attempted: item.LatestKnownChapter != nil}
	if latestChapter.Attempted {
		chapterURL, resolved, waitingChapterURL := h.chapterLinks.Lookup(chapterKey, chapterBaseURL, *item.LatestKnownChapter, chapterAlternates, pageKey)
		latestChapter.URL = chapterURL
		latestChapter.Resolved = resolved
		if waitingChapterURL {
			waiting = true
		}
	}

	lastReadChapter := chapterLinkLookup{Attempted: item.LastReadChapter != nil}
	if lastReadChapter.Attempted {
		chapterURL, resolved, waitingChapterURL := h.chapterLinks.Lookup(chapterKey, chapterBaseURL, *item.LastReadChapter, chapterAlternates, pageKey)
		lastReadChapter.URL = chapterURL
		lastReadChapter.Resolved = resolved
		if waitingChapterURL {
			waiting = true
		}
	}

	// The cover is looked up against the primary source even under a pin: a
	// pin narrows where the user is sent to read, not which site's art the
	// card is allowed to show.
	coverURL, coverSourceKey, waitingCover := h.covers.Lookup(sourceKey, item.SourceURL, item.SourceItemID, alternates, pageKey)
	card.CoverURL = coverURL
	if waitingCover {
		waiting = true
	}

	// The source that reported the stored chapter number, when a poll has
	// recorded one.
	reporterSourceKey := ""
	if item.LatestChapterSourceID != nil {
		if reporter, found := sourceByID[*item.LatestChapterSourceID]; found {
			reporterSourceKey = strings.ToLower(strings.TrimSpace(reporter.Key))
		}
	}

	decision := decideTrackerLinks(trackerLinkInputs{
		PrimarySourceKey:  sourceKey,
		PrimarySourceURL:  item.SourceURL,
		Pinned:            pinned,
		Alternates:        alternates,
		LatestChapter:     latestChapter,
		LastReadChapter:   lastReadChapter,
		ReporterSourceKey: reporterSourceKey,
		CoverSourceKey:    coverSourceKey,
		SourceKeyForURL:   h.sourceKeyForURL,
	})

	card.LatestKnownChapterURL = decision.LatestChapterURL
	card.LatestKnownChapterSite = chapterSiteLabel(decision.LatestChapterSiteKey, sourceNameByKey)
	card.LastReadChapterURL = decision.LastReadChapterURL
	card.LastReadChapterSite = chapterSiteLabel(decision.LastReadChapterSiteKey, sourceNameByKey)
	card.HighlightURL = decision.HighlightURL

	// When a fallback source served this card, present that source rather than
	// the primary: a badge naming a site that supplied nothing, next to links
	// pointing somewhere else, is worse than no badge at all.
	if decision.ServingSourceFound {
		card.SourceURL = decision.ServingSource.SourceURL
		card.SourceLogoURL = strings.TrimSpace(sourceLogoBySourceID[decision.ServingSource.SourceID])
		if servingSource, found := sourceByID[decision.ServingSource.SourceID]; found {
			card.SourceLogoLabel = servingSource.Name
		}
	}

	return card, waiting
}

// chapterLinkLookup is what one chapter-link lookup came back with. Attempted
// separates "the tracker stores no such chapter number" from "a lookup ran and
// came back with nothing": only a lookup that ran replaces the link the card
// started with, so a tracker with no read progress keeps its series page rather
// than being handed an empty href.
type chapterLinkLookup struct {
	Attempted bool
	URL       string
	// Resolved marks a link the resolver confirmed opens the chapter, as
	// opposed to the series page it degrades to. Only a confirmed link says
	// which site is serving this card.
	Resolved bool
}

// trackerLinkInputs is everything the arbitration below needs, already resolved
// by the caller: the tracker's own sources, what the chapter and cover lookups
// answered, and who reported the stored chapter number.
type trackerLinkInputs struct {
	PrimarySourceKey string
	PrimarySourceURL string

	// Pinned is the source the reading links are pinned to, nil under auto.
	Pinned     *repository.TrackerSourceRef
	Alternates []repository.TrackerSourceRef

	LatestChapter   chapterLinkLookup
	LastReadChapter chapterLinkLookup

	// ReporterSourceKey is the site that reported the stored chapter number and
	// CoverSourceKey the one that supplied the art; both empty when unknown.
	ReporterSourceKey string
	CoverSourceKey    string

	// SourceKeyForURL names the site a resolved chapter link opens on. The
	// caller supplies it — the handler reads it off the connector registry —
	// so the arbitration below stays a pure function that can be exercised
	// without one. A nil lookup attributes no link to any site, which is what
	// an unrecognized host already means here.
	SourceKeyForURL func(rawURL string) string
}

// trackerLinkDecision is where one card sends the reader. The site keys are
// returned rather than display labels so the arbitration stays independent of
// how the sources happen to be named.
type trackerLinkDecision struct {
	LatestChapterURL     string
	LatestChapterSiteKey string

	LastReadChapterURL     string
	LastReadChapterSiteKey string

	HighlightURL string

	ServingSourceKey   string
	ServingSource      repository.TrackerSourceRef
	ServingSourceFound bool
}

// decideTrackerLinks arbitrates which site a card sends the reader to. It is
// deliberately free of the handler, the database and the network: this is the
// policy, and it is the part worth pinning down in tests.
//
// The site holding the newest chapter is where the user goes to read, so it
// decides which source the card presents. The cover used to decide it, which is
// the weakest signal on the card: art resolves from a different endpoint than
// chapters do, so a card could badge one site while its chapter links opened
// another.
//
// The order is:
//   - Every link starts at the reading base — the pinned source's series page,
//     or the primary's under auto.
//   - A chapter lookup that ran replaces its own link, confirmed or not.
//   - Under auto, a latest-chapter link that resolved nowhere degrades to the
//     reporter's series page when the tracker carries one: the number on the
//     card is that site's claim, so its page is where the chapter can actually
//     be seen.
//   - Under auto, the open-to-read button follows the site the latest-chapter
//     link was confirmed on: a MangaUpdates-primary tracker whose chapters open
//     on MangaFire should open its series there too. A degraded link does not
//     move it, having confirmed nothing.
//   - The badge follows the strongest signal available: the site whose chapter
//     link was confirmed, else the site that reported the chapter number, else
//     whoever supplied the cover art — the weakest signal on the card, but
//     better than badging a primary that served nothing.
func decideTrackerLinks(in trackerLinkInputs) trackerLinkDecision {
	sourceKeyForURL := in.SourceKeyForURL
	if sourceKeyForURL == nil {
		sourceKeyForURL = func(string) string { return "" }
	}

	readingBaseURL := in.PrimarySourceURL
	if in.Pinned != nil {
		readingBaseURL = in.Pinned.SourceURL
	}

	decision := trackerLinkDecision{
		LatestChapterURL:   readingBaseURL,
		LastReadChapterURL: readingBaseURL,
		HighlightURL:       readingBaseURL,
	}

	confirmedChapterSiteKey := ""
	if in.LatestChapter.Attempted {
		decision.LatestChapterURL = in.LatestChapter.URL
		if in.LatestChapter.Resolved {
			confirmedChapterSiteKey = sourceKeyForURL(in.LatestChapter.URL)
			decision.LatestChapterSiteKey = confirmedChapterSiteKey
		}
	}

	if in.Pinned == nil && in.LatestChapter.Attempted && !in.LatestChapter.Resolved && in.ReporterSourceKey != "" {
		if seriesURL, found := linkedAlternateURL(in.Alternates, in.ReporterSourceKey); found {
			decision.LatestChapterURL = seriesURL
			decision.LatestChapterSiteKey = in.ReporterSourceKey
		}
	}

	if in.Pinned == nil && confirmedChapterSiteKey != "" && confirmedChapterSiteKey != in.PrimarySourceKey {
		if seriesURL, found := linkedAlternateURL(in.Alternates, confirmedChapterSiteKey); found {
			decision.HighlightURL = seriesURL
		}
	}

	if in.LastReadChapter.Attempted {
		decision.LastReadChapterURL = in.LastReadChapter.URL
		if in.LastReadChapter.Resolved {
			decision.LastReadChapterSiteKey = sourceKeyForURL(in.LastReadChapter.URL)
		}
	}

	decision.ServingSourceKey = confirmedChapterSiteKey
	if decision.ServingSourceKey == "" {
		decision.ServingSourceKey = in.ReporterSourceKey
	}
	if decision.ServingSourceKey == "" {
		decision.ServingSourceKey = in.CoverSourceKey
	}
	decision.ServingSource, decision.ServingSourceFound = findServingSource(decision.ServingSourceKey, in.PrimarySourceKey, in.Alternates)

	return decision
}

// linkedAlternateURL finds the series page a tracker carries for one source
// key. An alternate linked without a URL is skipped rather than accepted: it
// would turn a working link into an empty href.
func linkedAlternateURL(alternates []repository.TrackerSourceRef, sourceKey string) (string, bool) {
	for _, alternate := range alternates {
		if !strings.EqualFold(strings.TrimSpace(alternate.SourceKey), sourceKey) {
			continue
		}
		if strings.TrimSpace(alternate.SourceURL) == "" {
			continue
		}
		return alternate.SourceURL, true
	}
	return "", false
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

// buildTrackerSiteLinks takes homeURLForKey rather than reading the connector
// metadata itself, so the shortcut row can be built without a registry.
func buildTrackerSiteLinks(sources []models.Source, sourceLogoBySourceID map[int64]string, trackerCounts map[int64]int, homeURLForKey func(string) string) ([]trackerSiteLinkView, []trackerSiteLinkView) {
	type rankedLink struct {
		view  trackerSiteLinkView
		count int
	}

	ranked := make([]rankedLink, 0, len(sources))
	for _, source := range sources {
		homeURL := homeURLForKey(source.Key)
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
