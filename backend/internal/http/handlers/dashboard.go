package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

type DashboardHandler struct {
	trackerRepo        *repository.TrackerRepository
	sourceRepo         *repository.SourceRepository
	profileRepo        *repository.ProfileRepository
	profileResolver    *profileContextResolver
	registry           *connectors.Registry
	coverCache         map[string]coverCacheEntry
	cacheMu            sync.RWMutex
	coverFetchMu       sync.Mutex
	coverInFlight      map[string]bool
	coverFetchSem      chan struct{}
	chapterURLCache    map[string]chapterURLCacheEntry
	chapterURLCacheMu  sync.RWMutex
	chapterURLFetchMu  sync.Mutex
	chapterURLInFlight map[string]bool
	chapterURLFetchSem chan struct{}
	activePageMu       sync.RWMutex
	activePageKey      string
	templates          *template.Template
	templateOnce       sync.Once
	templateErr        error
}

type coverCacheEntry struct {
	CoverURL  string
	Found     bool
	ExpiresAt time.Time
}

type chapterURLCacheEntry struct {
	ChapterURL string
	Found      bool
	ExpiresAt  time.Time
}

var allowedTagIconKeys = map[string]bool{
	"icon_1": true,
	"icon_2": true,
	"icon_3": true,
}

var tagIconKeysOrdered = []string{"icon_1", "icon_2", "icon_3"}

type dashboardPageData struct {
	Statuses      []string
	Sorts         []string
	Profiles      []models.Profile
	ActiveProfile models.Profile
	RenameValue   string
	ProfileTags   []models.CustomTag
}

type trackersPartialData struct {
	Trackers      []trackerCardView
	ViewMode      string
	Page          int
	PrevPage      int
	NextPage      int
	TotalPages    int
	PageNumbers   []int
	HasPrevPage   bool
	HasNextPage   bool
	PendingCovers bool
	RefreshKey    string
}

type trackerOOBResponseData struct {
	ViewMode        string
	ReplaceCard     *trackerCardView
	PrependCard     *trackerCardView
	DeleteTrackerID int64
}

type trackerCardView struct {
	ID                     int64
	Title                  string
	Status                 string
	StatusLabel            string
	Tags                   []trackerTagView
	HiddenTagCount         int
	TagIcons               []trackerTagIconView
	SourceURL              string
	LatestKnownChapterURL  string
	LastReadChapterURL     string
	CoverURL               string
	LatestKnownChapter     string
	LatestReleaseAgo       string
	LastCheckedAgo         string
	LastReadChapter        string
	LastReadAgo            string
	LatestReleaseFormatted string
	UpdatedAtFormatted     string
	LastCheckedFormatted   string
	SourceItemID           *string
	LatestKnownChapterRaw  *float64
	LastReadChapterRaw     *float64
}

type trackerTagView struct {
	ID       int64
	Name     string
	IconKey  *string
	IconPath *string
}

type trackerTagIconView struct {
	TagName  string
	IconPath string
}

type trackerFormData struct {
	Mode          string
	Tracker       *models.Tracker
	Sources       []models.Source
	LinkedSources []models.TrackerSource
	ProfileTags   []models.CustomTag
	TrackerTags   []models.CustomTag
	TagIconKeys   []string
}

type trackerSearchResultsData struct {
	Items      []connectors.MangaResult
	Query      string
	Error      string
	SourceID   int64
	SourceName string
	Intent     string
}

type profileMenuData struct {
	Profiles          []models.Profile
	ActiveProfile     models.Profile
	RenameValue       string
	ProfileTags       []models.CustomTag
	TagIconKeys       []string
	AvailableIconKeys []string
	Message           string
}

type profileFilterTagsData struct {
	ProfileTags []models.CustomTag
}

func NewDashboardHandler(db *sql.DB, registry *connectors.Registry) *DashboardHandler {
	if registry == nil {
		registry = connectors.NewRegistry()
	}
	return &DashboardHandler{
		trackerRepo:        repository.NewTrackerRepository(db),
		sourceRepo:         repository.NewSourceRepository(db),
		profileRepo:        repository.NewProfileRepository(db),
		profileResolver:    newProfileContextResolver(db),
		registry:           registry,
		coverCache:         make(map[string]coverCacheEntry),
		coverInFlight:      make(map[string]bool),
		coverFetchSem:      make(chan struct{}, 8),
		chapterURLCache:    make(map[string]chapterURLCacheEntry),
		chapterURLInFlight: make(map[string]bool),
		chapterURLFetchSem: make(chan struct{}, 10),
	}
}

func (h *DashboardHandler) Page(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	c.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Set("Pragma", "no-cache")
	c.Set("Expires", "0")
	data := dashboardPageData{
		Statuses:      []string{"all", "reading", "completed", "on_hold", "dropped", "plan_to_read"},
		Sorts:         []string{"latest_known_chapter", "last_read_at"},
		Profiles:      profiles,
		ActiveProfile: *activeProfile,
		RenameValue:   activeProfile.Name,
		ProfileTags:   profileTags,
	}
	return h.render(c, "dashboard_page.html", data)
}

func (h *DashboardHandler) RenameProfileFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	name := strings.TrimSpace(c.FormValue("profile_name"))
	if name == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Profile name is required")
	}
	if len(name) > 40 {
		return c.Status(fiber.StatusBadRequest).SendString("Profile name must be 40 characters or less")
	}

	if _, err := h.profileRepo.Rename(activeProfile.ID, name); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to rename profile")
	}

	return c.Redirect("/dashboard?profile="+url.QueryEscape(activeProfile.Key), fiber.StatusSeeOther)
}

func (h *DashboardHandler) ProfileMenuModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
	})
}

func (h *DashboardHandler) ProfileFilterTagsPartial(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	return h.render(c, "profile_filter_tags_partial.html", profileFilterTagsData{ProfileTags: profileTags})
}

func (h *DashboardHandler) SwitchProfileFromMenu(c *fiber.Ctx) error {
	profileKey := strings.TrimSpace(string(c.Request().PostArgs().Peek("profile")))
	if profileKey == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Profile is required")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	for _, profile := range profiles {
		if profile.Key == profileKey {
			return c.Redirect("/dashboard?profile="+url.QueryEscape(profileKey), fiber.StatusSeeOther)
		}
	}

	return c.Status(fiber.StatusBadRequest).SendString("Selected profile does not exist")
}

func (h *DashboardHandler) CreateTagFromMenu(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	tagName := strings.TrimSpace(c.FormValue("tag_name"))
	if tagName == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Tag name is required")
	}
	if len(tagName) > 40 {
		return c.Status(fiber.StatusBadRequest).SendString("Tag name must be 40 characters or less")
	}

	var iconKey *string
	if rawIcon := strings.TrimSpace(c.FormValue("icon_key")); rawIcon != "" {
		if !allowedTagIconKeys[rawIcon] {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid icon")
		}
		iconKey = &rawIcon
	}

	if _, err := h.trackerRepo.CreateProfileTag(activeProfile.ID, tagName, iconKey); err != nil {
		lowerErr := strings.ToLower(err.Error())
		if strings.Contains(lowerErr, "unique") {
			if iconKey != nil {
				return c.Status(fiber.StatusBadRequest).SendString("That icon is already used by another tag")
			}
			return c.Status(fiber.StatusBadRequest).SendString("A tag with that name already exists")
		}
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save tag")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	c.Set("HX-Trigger", `{"trackersChanged":true,"profileTagsChanged":true}`)
	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
		Message:           "Tag saved",
	})
}

func (h *DashboardHandler) DeleteTagFromMenu(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	tagID, err := strconv.ParseInt(strings.TrimSpace(c.FormValue("tag_id")), 10, 64)
	if err != nil || tagID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tag")
	}

	deleted, err := h.trackerRepo.DeleteProfileTag(activeProfile.ID, tagID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to delete tag")
	}
	if !deleted {
		return c.Status(fiber.StatusBadRequest).SendString("Tag not found")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	c.Set("HX-Trigger", `{"trackersChanged":true,"profileTagsChanged":true}`)
	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
		Message:           "Tag deleted",
	})
}

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

	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}

	sourceKeyByID := make(map[int64]string, len(sources))
	for _, source := range sources {
		sourceKeyByID[source.ID] = source.Key
	}

	cards, pendingCovers := h.buildTrackerCards(items, sourceKeyByID, refreshKey)

	return h.render(c, "trackers_partial.html", trackersPartialData{
		Trackers:      cards,
		ViewMode:      viewMode,
		Page:          page,
		PrevPage:      max(1, page-1),
		NextPage:      min(totalPages, page+1),
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

func (h *DashboardHandler) buildTrackerCards(items []models.Tracker, sourceKeyByID map[int64]string, pageKey string) ([]trackerCardView, bool) {
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
		} else if item.LastCheckedAt != nil {
			card.LatestReleaseFormatted = item.LastCheckedAt.Format("2006-01-02 15:04")
			card.LatestReleaseAgo = relativeTime(*item.LastCheckedAt)
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

		sourceKey := sourceKeyByID[item.SourceID]

		if item.LatestKnownChapter != nil {
			latestChapterURL, waitingLatestChapterURL := h.getCachedOrQueueChapterURL(sourceKey, item.SourceURL, *item.LatestKnownChapter, pageKey)
			card.LatestKnownChapterURL = latestChapterURL
			if waitingLatestChapterURL {
				pendingCovers = true
			}
		}

		if item.LastReadChapter != nil {
			lastReadChapterURL, waitingLastReadChapterURL := h.getCachedOrQueueChapterURL(sourceKey, item.SourceURL, *item.LastReadChapter, pageKey)
			card.LastReadChapterURL = lastReadChapterURL
			if waitingLastReadChapterURL {
				pendingCovers = true
			}
		}

		coverURL, waitingCover := h.getCachedOrQueueCover(sourceKey, item.SourceURL, item.SourceItemID, pageKey)
		card.CoverURL = coverURL
		if waitingCover {
			pendingCovers = true
		}

		cards = append(cards, card)
	}

	return cards, pendingCovers
}

func (h *DashboardHandler) getCachedOrQueueCover(sourceKey, sourceURL string, sourceItemID *string, pageKey string) (string, bool) {
	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return "", false
	}

	cacheKey := buildCoverCacheKey(trimmedSourceKey, sourceURL, sourceItemID)
	if cachedURL, found, ok := h.getCachedCover(cacheKey); ok {
		if found {
			return cachedURL, false
		}
		h.queueCoverFetch(trimmedSourceKey, sourceURL, sourceItemID, cacheKey, pageKey)
		return "", true
	}

	if strings.TrimSpace(sourceURL) == "" {
		h.setCachedCover(cacheKey, "", false, 2*time.Minute)
		return "", false
	}

	h.queueCoverFetch(trimmedSourceKey, sourceURL, sourceItemID, cacheKey, pageKey)
	return "", true
}

func (h *DashboardHandler) queueCoverFetch(sourceKey, sourceURL string, sourceItemID *string, cacheKey string, pageKey string) {
	h.coverFetchMu.Lock()
	if h.coverInFlight[cacheKey] {
		h.coverFetchMu.Unlock()
		return
	}
	h.coverInFlight[cacheKey] = true
	h.coverFetchMu.Unlock()

	go func() {
		h.coverFetchSem <- struct{}{}
		defer func() {
			<-h.coverFetchSem
			h.coverFetchMu.Lock()
			delete(h.coverInFlight, cacheKey)
			h.coverFetchMu.Unlock()
		}()

		if pageKey != "" && !h.isActiveTrackersPageKey(pageKey) {
			return
		}

		_, _ = h.fetchCoverURL(context.Background(), sourceKey, sourceURL, sourceItemID)
	}()
}

func (h *DashboardHandler) getCachedOrQueueChapterURL(sourceKey, sourceURL string, chapter float64, pageKey string) (string, bool) {
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
		h.queueChapterURLResolve(trimmedSourceKey, trimmedSourceURL, chapter, cacheKey, pageKey)
		return trimmedSourceURL, true
	}

	h.queueChapterURLResolve(trimmedSourceKey, trimmedSourceURL, chapter, cacheKey, pageKey)
	return trimmedSourceURL, true
}

func (h *DashboardHandler) queueChapterURLResolve(sourceKey, sourceURL string, chapter float64, cacheKey string, pageKey string) {
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

		_, _ = h.fetchChapterURL(sourceKey, sourceURL, chapter)
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

func (h *DashboardHandler) fetchChapterURL(sourceKey, sourceURL string, chapter float64) (string, error) {
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

	connector, ok := h.registry.Get(trimmedSourceKey)
	if !ok {
		h.setCachedChapterURL(cacheKey, "", false, 30*time.Minute)
		return trimmedSourceURL, fmt.Errorf("connector not found")
	}

	resolver, ok := connector.(connectors.ChapterURLResolver)
	if !ok {
		h.setCachedChapterURL(cacheKey, "", false, 30*time.Minute)
		return trimmedSourceURL, fmt.Errorf("chapter resolver not supported")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	chapterURL, err := resolver.ResolveChapterURL(ctx, trimmedSourceURL, chapter)
	if err != nil {
		h.setCachedChapterURL(cacheKey, "", false, 2*time.Minute)
		return trimmedSourceURL, fmt.Errorf("resolve chapter url: %w", err)
	}

	chapterURL = strings.TrimSpace(chapterURL)
	if chapterURL == "" {
		h.setCachedChapterURL(cacheKey, "", false, 30*time.Minute)
		return trimmedSourceURL, fmt.Errorf("chapter url empty")
	}

	h.setCachedChapterURL(cacheKey, chapterURL, true, 12*time.Hour)
	return chapterURL, nil
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

func (h *DashboardHandler) NewTrackerModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:          "create",
		Sources:       sources,
		LinkedSources: []models.TrackerSource{},
		ProfileTags:   profileTags,
		TrackerTags:   []models.CustomTag{},
		TagIconKeys:   tagIconKeysOrdered,
	})
}

func (h *DashboardHandler) EmptyModal(c *fiber.Ctx) error {
	return h.render(c, "empty_modal.html", nil)
}

func (h *DashboardHandler) SearchSourceTitles(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))
	intent := strings.TrimSpace(c.Query("intent"))
	if intent == "" {
		intent = "primary"
	}
	if query == "" {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Intent: intent})
	}

	sourceID, err := strconv.ParseInt(strings.TrimSpace(c.Query("source_id")), 10, 64)
	if err != nil || sourceID <= 0 {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Select a source first", Intent: intent})
	}

	source, err := h.sourceRepo.GetByID(sourceID)
	if err != nil {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Failed to resolve source", Intent: intent})
	}
	if source == nil || !source.Enabled {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Source not found or disabled", Intent: intent})
	}

	connector, ok := h.registry.Get(source.Key)
	if !ok {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "No connector registered for selected source", Intent: intent})
	}

	searchTimeout := 5 * time.Second
	if source.Key == "mangaplus" || source.Key == "mangafire" {
		searchTimeout = 12 * time.Second
	}

	ctx, cancel := context.WithTimeout(c.Context(), searchTimeout)
	defer cancel()

	if source.Key == "mangafire" {
		mangaURL, ok := extractMangaFireMangaURL(query)
		if !ok {
			return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
				Query:      query,
				SourceID:   source.ID,
				SourceName: source.Name,
				Intent:     intent,
				Error:      "MangaFire search requires a full manga URL (https://mangafire.to/manga/{id})",
			})
		}

		resolved, resolveErr := connector.ResolveByURL(ctx, mangaURL)
		if resolveErr != nil || resolved == nil {
			message := "Failed to resolve MangaFire URL"
			if resolveErr != nil {
				message = "Failed to resolve MangaFire URL: " + resolveErr.Error()
			}
			return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
				Query:      query,
				SourceID:   source.ID,
				SourceName: source.Name,
				Intent:     intent,
				Error:      message,
			})
		}

		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
			Items:      []connectors.MangaResult{*resolved},
			Query:      query,
			SourceID:   source.ID,
			SourceName: source.Name,
			Intent:     intent,
		})
	}

	results, err := connector.SearchByTitle(ctx, query, 8)
	if err != nil {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Search failed for this source: " + err.Error(), SourceID: source.ID, SourceName: source.Name, Intent: intent})
	}

	return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Items: results, Query: query, SourceID: source.ID, SourceName: source.Name, Intent: intent})
}

func extractMangaFireMangaURL(query string) (string, bool) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", false
	}

	if !strings.HasPrefix(strings.ToLower(trimmed), "http://") && !strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		return "", false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Hostname(), "mangafire.to") && !strings.EqualFold(parsed.Hostname(), "www.mangafire.to") {
		return "", false
	}
	if !strings.HasPrefix(strings.ToLower(parsed.Path), "/manga/") {
		return "", false
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func (h *DashboardHandler) EditTrackerModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	tracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load tracker")
	}
	if tracker == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}

	linkedSources, err := h.trackerRepo.ListTrackerSources(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sources")
	}
	if len(linkedSources) == 0 {
		sourceName := ""
		for _, source := range sources {
			if source.ID == tracker.SourceID {
				sourceName = source.Name
				break
			}
		}
		linkedSources = append(linkedSources, models.TrackerSource{
			TrackerID:    tracker.ID,
			SourceID:     tracker.SourceID,
			SourceName:   sourceName,
			SourceItemID: tracker.SourceItemID,
			SourceURL:    tracker.SourceURL,
		})
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:          "edit",
		Tracker:       tracker,
		Sources:       sources,
		LinkedSources: linkedSources,
		ProfileTags:   profileTags,
		TrackerTags:   tracker.Tags,
		TagIconKeys:   tagIconKeysOrdered,
	})
}

func (h *DashboardHandler) CreateFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	tracker, err := parseTrackerFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	tracker.ProfileID = activeProfile.ID

	h.enrichTrackerFromSource(c.Context(), tracker)

	now := time.Now().UTC()
	tracker.LastCheckedAt = &now

	exists, err := h.trackerRepo.SourceExists(tracker.SourceID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to validate source")
	}
	if !exists {
		return c.Status(fiber.StatusBadRequest).SendString("Selected source does not exist")
	}

	created, err := h.trackerRepo.Create(tracker)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to create tracker")
	}

	tagIDs, err := parseTagIDsFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	if created != nil {
		if err := h.trackerRepo.ReplaceTrackerTags(activeProfile.ID, created.ID, tagIDs); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to save tracker tags")
		}
	}

	c.Set("HX-Trigger", `{"trackersChanged":true}`)
	return h.render(c, "empty_modal.html", nil)
}

func (h *DashboardHandler) enrichTrackerFromSource(parent context.Context, tracker *models.Tracker) {
	if tracker == nil || strings.TrimSpace(tracker.SourceURL) == "" || tracker.SourceID <= 0 {
		return
	}

	source, err := h.sourceRepo.GetByID(tracker.SourceID)
	if err != nil || source == nil || !source.Enabled {
		return
	}

	connector, ok := h.registry.Get(source.Key)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	resolved, err := connector.ResolveByURL(ctx, tracker.SourceURL)
	if err != nil || resolved == nil {
		return
	}

	if tracker.SourceItemID == nil {
		resolvedItemID := strings.TrimSpace(resolved.SourceItemID)
		if resolvedItemID != "" {
			tracker.SourceItemID = &resolvedItemID
		}
	}

	if tracker.LatestKnownChapter == nil && resolved.LatestChapter != nil {
		tracker.LatestKnownChapter = resolved.LatestChapter
	}

	if resolved.LastUpdatedAt != nil {
		updatedAt := resolved.LastUpdatedAt.UTC()
		tracker.LatestReleaseAt = &updatedAt
	}
}

func (h *DashboardHandler) UpdateFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	viewMode := normalizeViewMode(c.FormValue("view_mode", "grid"))

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	existingTracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load tracker")
	}
	if existingTracker == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	tracker, err := parseTrackerFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	tracker.LastCheckedAt = existingTracker.LastCheckedAt
	tracker.LatestReleaseAt = existingTracker.LatestReleaseAt
	tracker.ProfileID = activeProfile.ID

	linkedSources, err := parseLinkedSourcesFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	primaryFromForm := models.TrackerSource{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}

	uniqueSources := dedupeTrackerSources(linkedSources)
	if len(uniqueSources) == 0 {
		uniqueSources = dedupeTrackerSources([]models.TrackerSource{primaryFromForm})
	}

	for _, source := range uniqueSources {
		exists, err := h.trackerRepo.SourceExists(source.SourceID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to validate linked source")
		}
		if !exists {
			return c.Status(fiber.StatusBadRequest).SendString("One of the linked sources does not exist")
		}
	}

	primarySource, latestKnownChapter, latestReleaseAt := h.selectPrimaryTrackerSource(c.Context(), uniqueSources)
	tracker.SourceID = primarySource.SourceID
	tracker.SourceItemID = primarySource.SourceItemID
	tracker.SourceURL = primarySource.SourceURL
	if latestKnownChapter != nil {
		tracker.LatestKnownChapter = latestKnownChapter
	}
	if latestReleaseAt != nil {
		tracker.LatestReleaseAt = latestReleaseAt
	}

	exists, err := h.trackerRepo.SourceExists(tracker.SourceID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to validate source")
	}
	if !exists {
		return c.Status(fiber.StatusBadRequest).SendString("Selected source does not exist")
	}

	updated, err := h.trackerRepo.Update(activeProfile.ID, id, tracker)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to update tracker")
	}
	if updated == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	if err := h.trackerRepo.ReplaceTrackerSources(activeProfile.ID, id, uniqueSources); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save linked sources")
	}

	tagIDs, err := parseTagIDsFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	if err := h.trackerRepo.ReplaceTrackerTags(activeProfile.ID, id, tagIDs); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save tracker tags")
	}

	fullTracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil || fullTracker == nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	sourceKeyByID, err := h.listSourceKeys()
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	cards, _ := h.buildTrackerCards([]models.Tracker{*fullTracker}, sourceKeyByID, "")
	if len(cards) == 0 {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{
		ViewMode:    viewMode,
		ReplaceCard: &cards[0],
	})
}

func (h *DashboardHandler) DeleteFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	deleted, err := h.trackerRepo.Delete(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to delete tracker")
	}
	if !deleted {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{DeleteTrackerID: id})
}

func (h *DashboardHandler) SetLastReadFromCard(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	viewMode := normalizeViewMode(c.FormValue("view_mode", c.Query("view", "grid")))

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	tracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load tracker")
	}
	if tracker == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	if tracker.LatestKnownChapter != nil {
		_, err := h.trackerRepo.UpdateLastReadChapter(activeProfile.ID, id, tracker.LatestKnownChapter)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to update tracker")
		}
	}

	updatedTracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil || updatedTracker == nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	sourceKeyByID, err := h.listSourceKeys()
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	cards, _ := h.buildTrackerCards([]models.Tracker{*updatedTracker}, sourceKeyByID, "")
	if len(cards) == 0 {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{
		ViewMode:    viewMode,
		ReplaceCard: &cards[0],
	})
}

func (h *DashboardHandler) listSourceKeys() (map[int64]string, error) {
	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return nil, err
	}

	sourceKeyByID := make(map[int64]string, len(sources))
	for _, source := range sources {
		sourceKeyByID[source.ID] = source.Key
	}

	return sourceKeyByID, nil
}

func parseTrackerFromForm(c *fiber.Ctx) (*models.Tracker, error) {
	title := strings.TrimSpace(c.FormValue("title"))
	if title == "" {
		return nil, fmt.Errorf("Title is required")
	}

	sourceID, err := strconv.ParseInt(strings.TrimSpace(c.FormValue("source_id")), 10, 64)
	if err != nil || sourceID <= 0 {
		return nil, fmt.Errorf("Valid source is required")
	}

	sourceURL := strings.TrimSpace(c.FormValue("source_url"))
	if sourceURL == "" {
		return nil, fmt.Errorf("Source URL is required")
	}

	status := strings.TrimSpace(c.FormValue("status"))
	if status == "" {
		status = "reading"
	}

	var sourceItemID *string
	if raw := strings.TrimSpace(c.FormValue("source_item_id")); raw != "" {
		sourceItemID = &raw
	}

	lastRead, err := parseOptionalFloat(c.FormValue("last_read_chapter"))
	if err != nil {
		return nil, fmt.Errorf("Invalid last read chapter")
	}
	latestKnown, err := parseOptionalFloat(c.FormValue("latest_known_chapter"))
	if err != nil {
		return nil, fmt.Errorf("Invalid latest known chapter")
	}

	latestReleaseAt, err := parseOptionalRFC3339Time(c.FormValue("latest_release_at"))
	if err != nil {
		return nil, fmt.Errorf("Invalid latest release date")
	}

	return &models.Tracker{
		Title:              title,
		SourceID:           sourceID,
		SourceItemID:       sourceItemID,
		SourceURL:          sourceURL,
		Status:             status,
		LastReadChapter:    lastRead,
		LatestKnownChapter: latestKnown,
		LatestReleaseAt:    latestReleaseAt,
	}, nil
}

func parseOptionalFloat(raw string) (*float64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func parseOptionalRFC3339Time(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, err
	}
	utc := value.UTC()
	return &utc, nil
}

func parseTagIDsFromForm(c *fiber.Ctx) ([]int64, error) {
	rawValues := c.Context().PostArgs().PeekMulti("tag_ids")
	if len(rawValues) == 0 {
		return []int64{}, nil
	}

	ids := make([]int64, 0, len(rawValues))
	seen := make(map[int64]bool, len(rawValues))
	for _, raw := range rawValues {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" {
			continue
		}
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("Invalid tag selection")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}

	return ids, nil
}

func parseLinkedSourcesFromForm(c *fiber.Ctx) ([]models.TrackerSource, error) {
	raw := strings.TrimSpace(c.FormValue("linked_sources_json"))
	if raw == "" {
		return []models.TrackerSource{}, nil
	}

	type linkedSourcePayload struct {
		SourceID     int64   `json:"sourceId"`
		SourceItemID *string `json:"sourceItemId"`
		SourceURL    string  `json:"sourceUrl"`
	}

	var payload []linkedSourcePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("Invalid linked sources payload")
	}

	items := make([]models.TrackerSource, 0, len(payload))
	for _, item := range payload {
		sourceURL := strings.TrimSpace(item.SourceURL)
		if item.SourceID <= 0 || sourceURL == "" {
			continue
		}
		if item.SourceItemID != nil && strings.TrimSpace(*item.SourceItemID) == "" {
			item.SourceItemID = nil
		}
		items = append(items, models.TrackerSource{
			SourceID:     item.SourceID,
			SourceItemID: item.SourceItemID,
			SourceURL:    sourceURL,
		})
	}

	return items, nil
}

func dedupeTrackerSources(items []models.TrackerSource) []models.TrackerSource {
	seen := make(map[string]bool, len(items))
	out := make([]models.TrackerSource, 0, len(items))
	for _, item := range items {
		sourceURL := strings.TrimSpace(item.SourceURL)
		if item.SourceID <= 0 || sourceURL == "" {
			continue
		}
		key := fmt.Sprintf("%d|%s", item.SourceID, strings.ToLower(sourceURL))
		if seen[key] {
			continue
		}
		seen[key] = true
		item.SourceURL = sourceURL
		out = append(out, item)
	}
	return out
}

func parseTagNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		normalized := strings.ToLower(tag)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, tag)
	}
	return out
}

func parseTagNamesFromQuery(c *fiber.Ctx) []string {
	queryValues := c.Context().QueryArgs().PeekMulti("tags")
	if len(queryValues) == 0 {
		return parseTagNames(c.Query("tags"))
	}

	values := make([]string, 0, len(queryValues))
	for _, value := range queryValues {
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}

	if len(values) == 0 {
		return nil
	}

	return parseTagNames(strings.Join(values, ","))
}

func toTrackerTagView(tags []models.CustomTag) []trackerTagView {
	items := make([]trackerTagView, 0, len(tags))
	for _, tag := range tags {
		items = append(items, trackerTagView{
			ID:       tag.ID,
			Name:     tag.Name,
			IconKey:  tag.IconKey,
			IconPath: tag.IconPath,
		})
	}
	return items
}

func toTrackerTagIcons(tags []models.CustomTag) []trackerTagIconView {
	icons := make([]trackerTagIconView, 0, len(tags))
	for _, tag := range tags {
		if tag.IconPath == nil {
			continue
		}
		icons = append(icons, trackerTagIconView{TagName: tag.Name, IconPath: *tag.IconPath})
	}
	return icons
}

func prioritizeTrackerTags(tags []trackerTagView, maxVisible int) ([]trackerTagView, int) {
	if maxVisible <= 0 || len(tags) == 0 {
		return nil, len(tags)
	}

	withIcon := make([]trackerTagView, 0, len(tags))
	withoutIcon := make([]trackerTagView, 0, len(tags))
	for _, tag := range tags {
		if tag.IconPath != nil {
			withIcon = append(withIcon, tag)
			continue
		}
		withoutIcon = append(withoutIcon, tag)
	}

	ordered := make([]trackerTagView, 0, len(tags))
	ordered = append(ordered, withIcon...)
	ordered = append(ordered, withoutIcon...)

	if len(ordered) <= maxVisible {
		return ordered, 0
	}

	return ordered[:maxVisible], len(ordered) - maxVisible
}

func (h *DashboardHandler) selectPrimaryTrackerSource(parent context.Context, sources []models.TrackerSource) (models.TrackerSource, *float64, *time.Time) {
	if len(sources) == 0 {
		return models.TrackerSource{}, nil, nil
	}

	bestIndex := 0
	var bestChapter *float64
	var bestReleaseAt *time.Time

	for idx := range sources {
		source := &sources[idx]
		resolved, err := h.resolveLinkedSource(parent, source.SourceID, source.SourceURL)
		if err != nil || resolved == nil {
			continue
		}

		resolvedItemID := strings.TrimSpace(resolved.SourceItemID)
		if source.SourceItemID == nil && resolvedItemID != "" {
			source.SourceItemID = &resolvedItemID
		}

		if resolved.LatestChapter == nil {
			continue
		}

		resolvedChapter := *resolved.LatestChapter
		if bestChapter == nil || resolvedChapter > *bestChapter {
			bestIndex = idx
			bestChapter = &resolvedChapter
			if resolved.LastUpdatedAt != nil {
				resolvedReleaseAt := resolved.LastUpdatedAt.UTC()
				bestReleaseAt = &resolvedReleaseAt
			} else {
				bestReleaseAt = nil
			}
			continue
		}

		if resolvedChapter == *bestChapter && resolved.LastUpdatedAt != nil {
			if bestReleaseAt == nil || resolved.LastUpdatedAt.After(*bestReleaseAt) {
				bestIndex = idx
				resolvedReleaseAt := resolved.LastUpdatedAt.UTC()
				bestReleaseAt = &resolvedReleaseAt
			}
		}
	}

	return sources[bestIndex], bestChapter, bestReleaseAt
}

func (h *DashboardHandler) resolveLinkedSource(parent context.Context, sourceID int64, sourceURL string) (*connectors.MangaResult, error) {
	if sourceID <= 0 || strings.TrimSpace(sourceURL) == "" {
		return nil, fmt.Errorf("source is incomplete")
	}

	source, err := h.sourceRepo.GetByID(sourceID)
	if err != nil {
		return nil, err
	}
	if source == nil || !source.Enabled {
		return nil, fmt.Errorf("source unavailable")
	}

	connector, ok := h.registry.Get(source.Key)
	if !ok {
		return nil, fmt.Errorf("connector unavailable")
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	resolved, err := connector.ResolveByURL(ctx, strings.TrimSpace(sourceURL))
	if err != nil {
		return nil, err
	}

	return resolved, nil
}

func formatChapterLabel(chapter float64) string {
	return "Ch. " + strconv.FormatFloat(chapter, 'f', -1, 64)
}

func chapterInputValue(chapter *float64) string {
	if chapter == nil {
		return ""
	}
	if math.IsNaN(*chapter) || math.IsInf(*chapter, 0) {
		return ""
	}
	return strconv.FormatFloat(*chapter, 'f', -1, 64)
}

func textInputValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func timeInputValue(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func relativeTime(value time.Time) string {
	now := time.Now().UTC()
	target := value.UTC()
	if target.After(now) {
		return "just now"
	}

	delta := now.Sub(target)
	if delta < time.Minute {
		return "just now"
	}
	if delta < time.Hour {
		minutes := int(delta / time.Minute)
		return fmt.Sprintf("%d min ago", minutes)
	}
	if delta < 24*time.Hour {
		hours := int(delta / time.Hour)
		return fmt.Sprintf("%d hours ago", hours)
	}
	if delta < 30*24*time.Hour {
		days := int(delta / (24 * time.Hour))
		return fmt.Sprintf("%d days ago", days)
	}
	if delta < 365*24*time.Hour {
		months := int(delta / (30 * 24 * time.Hour))
		return fmt.Sprintf("%d months ago", months)
	}
	years := int(delta / (365 * 24 * time.Hour))
	return fmt.Sprintf("%d years ago", years)
}

func (h *DashboardHandler) fetchCoverURL(parent context.Context, sourceKey, sourceURL string, sourceItemID *string) (string, error) {
	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return "", fmt.Errorf("missing source key")
	}

	cacheKey := buildCoverCacheKey(trimmedSourceKey, sourceURL, sourceItemID)
	if cachedURL, found, ok := h.getCachedCover(cacheKey); ok {
		if found {
			return cachedURL, nil
		}
		return "", fmt.Errorf("cover not found")
	}

	resolvedURL := strings.TrimSpace(sourceURL)
	if resolvedURL == "" {
		h.setCachedCover(cacheKey, "", false, 2*time.Minute)
		return "", fmt.Errorf("missing source url")
	}

	tryKeys := make([]string, 0, 2)
	tryKeys = append(tryKeys, trimmedSourceKey)

	if fallbackKey := inferSourceKeyFromURL(resolvedURL); fallbackKey != "" && fallbackKey != trimmedSourceKey {
		tryKeys = append(tryKeys, fallbackKey)
	}

	for _, key := range tryKeys {
		coverURL, err := h.resolveCoverFromConnector(parent, key, resolvedURL)
		if err != nil {
			continue
		}
		if coverURL == "" {
			continue
		}

		h.setCachedCover(cacheKey, coverURL, true, 12*time.Hour)
		return coverURL, nil
	}

	h.setCachedCover(cacheKey, "", false, 2*time.Minute)
	return "", fmt.Errorf("cover not found")
}

func (h *DashboardHandler) resolveCoverFromConnector(parent context.Context, sourceKey, sourceURL string) (string, error) {
	connector, ok := h.registry.Get(strings.TrimSpace(sourceKey))
	if !ok {
		return "", fmt.Errorf("connector not found")
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, sourceURL)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("empty result")
	}

	return strings.TrimSpace(result.CoverImageURL), nil
}

func inferSourceKeyFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	switch {
	case strings.Contains(host, "mangadex"):
		return "mangadex"
	case strings.Contains(host, "mangafire"):
		return "mangafire"
	case strings.Contains(host, "mangaplus") || strings.Contains(host, "shueisha"):
		return "mangaplus"
	case strings.Contains(host, "asura"):
		return "asuracomic"
	case strings.Contains(host, "flame"):
		return "flamecomics"
	default:
		return ""
	}
}

func buildCoverCacheKey(sourceKey, sourceURL string, sourceItemID *string) string {
	itemID := ""
	if sourceItemID != nil {
		itemID = strings.TrimSpace(*sourceItemID)
	}

	base := strings.ToLower(strings.TrimSpace(sourceKey)) + "|"
	if itemID != "" {
		return base + "item:" + strings.ToLower(itemID)
	}

	trimmedURL := strings.TrimSpace(sourceURL)
	if trimmedURL != "" {
		return base + "url:" + strings.ToLower(trimmedURL)
	}

	return base + "missing"
}

func (h *DashboardHandler) getCachedCover(titleID string) (coverURL string, found bool, ok bool) {
	h.cacheMu.RLock()
	entry, exists := h.coverCache[titleID]
	h.cacheMu.RUnlock()
	if !exists {
		return "", false, false
	}

	if time.Now().UTC().After(entry.ExpiresAt) {
		h.cacheMu.Lock()
		delete(h.coverCache, titleID)
		h.cacheMu.Unlock()
		return "", false, false
	}

	return entry.CoverURL, entry.Found, true
}

func (h *DashboardHandler) setCachedCover(titleID, coverURL string, found bool, ttl time.Duration) {
	h.cacheMu.Lock()
	h.coverCache[titleID] = coverCacheEntry{
		CoverURL:  coverURL,
		Found:     found,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	h.cacheMu.Unlock()
}

func (h *DashboardHandler) render(c *fiber.Ctx, templateName string, data any) error {
	h.templateOnce.Do(func() {
		h.templates, h.templateErr = template.New("").Funcs(template.FuncMap{
			"chapterInputValue": chapterInputValue,
			"textInputValue":    textInputValue,
			"timeInputValue":    timeInputValue,
			"hasTagID":          hasTagID,
			"tagIconLabel":      tagIconLabel,
			"tagIconAssetPath":  tagIconAssetPath,
			"toJSON":            toJSON,
			"statusLabel":       statusLabel,
			"sortLabel":         sortLabel,
		}).ParseGlob("web/templates/*.html")
	})

	if h.templateErr != nil || h.templates == nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Template load error")
	}
	c.Type("html", "utf-8")
	return h.templates.ExecuteTemplate(c.Response().BodyWriter(), templateName, data)
}

func statusLabel(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "all":
		return "All statuses"
	case "on_hold":
		return "On hold"
	case "plan_to_read":
		return "Plan to read"
	default:
		return humanizeValueLabel(value)
	}
}

func sortLabel(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "last_read_at":
		return "Recently read"
	case "title":
		return "Title (A–Z)"
	case "created_at":
		return "Date added"
	case "last_checked_at":
		return "Last checked"
	case "latest_known_chapter":
		return "Latest chapter"
	default:
		return humanizeValueLabel(value)
	}
}

func humanizeValueLabel(value string) string {
	normalized := strings.TrimSpace(strings.ToLower(value))
	if normalized == "" {
		return "—"
	}
	parts := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(normalized))
	for index, part := range parts {
		if len(part) == 0 {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func toJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func hasTagID(tags []models.CustomTag, id int64) bool {
	for _, tag := range tags {
		if tag.ID == id {
			return true
		}
	}
	return false
}

func availableTagIconKeys(profileTags []models.CustomTag) []string {
	used := make(map[string]bool, len(profileTags))
	for _, tag := range profileTags {
		if tag.IconKey == nil {
			continue
		}
		iconKey := strings.TrimSpace(*tag.IconKey)
		if iconKey != "" {
			used[iconKey] = true
		}
	}

	available := make([]string, 0, len(tagIconKeysOrdered))
	for _, iconKey := range tagIconKeysOrdered {
		if used[iconKey] {
			continue
		}
		available = append(available, iconKey)
	}

	return available
}

func tagIconLabel(iconKey string) string {
	switch strings.TrimSpace(iconKey) {
	case "icon_1":
		return "Star"
	case "icon_2":
		return "Heart"
	case "icon_3":
		return "Flames"
	default:
		return "Icon"
	}
}

func tagIconAssetPath(iconKey string) string {
	switch strings.TrimSpace(iconKey) {
	case "icon_1":
		return "/assets/tag-icons/icon-star-gold.svg"
	case "icon_2":
		return "/assets/tag-icons/icon-red-heart.svg"
	case "icon_3":
		return "/assets/tag-icons/icon-flames.svg"
	default:
		return ""
	}
}
