package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"regexp"
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
	trackerRepo     *repository.TrackerRepository
	sourceRepo      *repository.SourceRepository
	profileRepo     *repository.ProfileRepository
	profileResolver *profileContextResolver
	registry        *connectors.Registry
	coverCache      map[string]coverCacheEntry
	cacheMu         sync.RWMutex
}

type coverCacheEntry struct {
	CoverURL  string
	Found     bool
	ExpiresAt time.Time
}

var mangafireMangaURLPattern = regexp.MustCompile(`(?i)https?://(?:www\.)?mangafire\.to/manga/[^\s"'<>]+`)

type dashboardPageData struct {
	Statuses      []string
	Sorts         []string
	Profiles      []models.Profile
	ActiveProfile models.Profile
	RenameValue   string
}

type trackersPartialData struct {
	Trackers []trackerCardView
	ViewMode string
}

type trackerCardView struct {
	ID                     int64
	Title                  string
	Status                 string
	StatusLabel            string
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

type trackerFormData struct {
	Mode          string
	Tracker       *models.Tracker
	Sources       []models.Source
	LinkedSources []models.TrackerSource
}

type trackerSearchResultsData struct {
	Items      []connectors.MangaResult
	Query      string
	Error      string
	SourceID   int64
	SourceName string
	Intent     string
}

func NewDashboardHandler(db *sql.DB, registry *connectors.Registry) *DashboardHandler {
	if registry == nil {
		registry = connectors.NewRegistry()
	}
	return &DashboardHandler{
		trackerRepo:     repository.NewTrackerRepository(db),
		sourceRepo:      repository.NewSourceRepository(db),
		profileRepo:     repository.NewProfileRepository(db),
		profileResolver: newProfileContextResolver(db),
		registry:        registry,
		coverCache:      make(map[string]coverCacheEntry),
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

	c.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Set("Pragma", "no-cache")
	c.Set("Expires", "0")
	data := dashboardPageData{
		Statuses:      []string{"all", "reading", "completed", "on_hold", "dropped", "plan_to_read"},
		Sorts:         []string{"latest_known_chapter", "last_read_at"},
		Profiles:      profiles,
		ActiveProfile: *activeProfile,
		RenameValue:   activeProfile.Name,
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

	viewMode := strings.TrimSpace(c.Query("view", "grid"))
	if viewMode != "grid" && viewMode != "list" {
		viewMode = "grid"
	}

	items, err := h.trackerRepo.List(repository.TrackerListOptions{
		ProfileID: activeProfile.ID,
		Statuses:  statuses,
		SortBy:    strings.TrimSpace(c.Query("sort", "latest_known_chapter")),
		Order:     strings.TrimSpace(c.Query("order", "desc")),
		Query:     strings.TrimSpace(c.Query("q")),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load trackers")
	}

	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}

	sourceKeyByID := make(map[int64]string, len(sources))
	for _, source := range sources {
		sourceKeyByID[source.ID] = source.Key
	}

	cards := make([]trackerCardView, 0, len(items))
	for _, item := range items {
		card := trackerCardView{
			ID:                     item.ID,
			Title:                  item.Title,
			Status:                 item.Status,
			StatusLabel:            statusLabel(item.Status),
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
			card.LatestKnownChapterURL = h.resolveChapterURL(c.Context(), sourceKey, item.SourceURL, *item.LatestKnownChapter)
		}

		if item.LastReadChapter != nil {
			card.LastReadChapterURL = h.resolveChapterURL(c.Context(), sourceKey, item.SourceURL, *item.LastReadChapter)
		}

		if coverURL, coverErr := h.fetchCoverURL(c.Context(), sourceKey, item.SourceURL, item.SourceItemID); coverErr == nil {
			card.CoverURL = coverURL
		}

		cards = append(cards, card)
	}

	return h.render(c, "trackers_partial.html", trackersPartialData{Trackers: cards, ViewMode: viewMode})
}

func (h *DashboardHandler) NewTrackerModal(c *fiber.Ctx) error {
	if _, err := h.profileResolver.Resolve(c); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}
	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:          "create",
		Sources:       sources,
		LinkedSources: []models.TrackerSource{},
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
		if mangaURL, ok := extractMangaFireMangaURL(query); ok {
			resolved, resolveErr := connector.ResolveByURL(ctx, mangaURL)
			if resolveErr == nil && resolved != nil {
				return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
					Items:      []connectors.MangaResult{*resolved},
					Query:      query,
					SourceID:   source.ID,
					SourceName: source.Name,
					Intent:     intent,
				})
			}
		}
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

	if strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", false
		}
		if strings.EqualFold(parsed.Hostname(), "mangafire.to") || strings.EqualFold(parsed.Hostname(), "www.mangafire.to") {
			if strings.HasPrefix(strings.ToLower(parsed.Path), "/manga/") {
				parsed.RawQuery = ""
				parsed.Fragment = ""
				return parsed.String(), true
			}
		}
	}

	match := mangafireMangaURLPattern.FindString(trimmed)
	if match == "" {
		return "", false
	}
	parsed, err := url.Parse(match)
	if err != nil {
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

	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:          "edit",
		Tracker:       tracker,
		Sources:       sources,
		LinkedSources: linkedSources,
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

	if _, err := h.trackerRepo.Create(tracker); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to create tracker")
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

	c.Set("HX-Trigger", `{"trackersChanged":true}`)
	return h.render(c, "empty_modal.html", nil)
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

	c.Set("HX-Trigger", `{"trackersChanged":true}`)
	return h.render(c, "empty_modal.html", nil)
}

func (h *DashboardHandler) SetLastReadFromCard(c *fiber.Ctx) error {
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

	if tracker.LatestKnownChapter != nil {
		_, err := h.trackerRepo.UpdateLastReadChapter(activeProfile.ID, id, tracker.LatestKnownChapter)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to update tracker")
		}
	}

	c.Set("HX-Trigger", `{"trackersChanged":true}`)
	return h.render(c, "empty_modal.html", nil)
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

		if bestChapter != nil && resolvedChapter == *bestChapter && resolved.LastUpdatedAt != nil {
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

func (h *DashboardHandler) resolveChapterURL(parent context.Context, sourceKey, sourceURL string, chapter float64) string {
	trimmedSourceURL := strings.TrimSpace(sourceURL)
	if trimmedSourceURL == "" {
		return ""
	}

	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return trimmedSourceURL
	}

	connector, ok := h.registry.Get(trimmedSourceKey)
	if !ok {
		return trimmedSourceURL
	}

	resolver, ok := connector.(connectors.ChapterURLResolver)
	if !ok {
		return trimmedSourceURL
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	chapterURL, err := resolver.ResolveChapterURL(ctx, trimmedSourceURL, chapter)
	if err != nil {
		return trimmedSourceURL
	}

	chapterURL = strings.TrimSpace(chapterURL)
	if chapterURL == "" {
		return trimmedSourceURL
	}

	return chapterURL
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

	connector, ok := h.registry.Get(trimmedSourceKey)
	if !ok {
		h.setCachedCover(cacheKey, "", false, 30*time.Minute)
		return "", fmt.Errorf("connector not found")
	}

	resolvedURL := strings.TrimSpace(sourceURL)
	if resolvedURL == "" {
		h.setCachedCover(cacheKey, "", false, 2*time.Minute)
		return "", fmt.Errorf("missing source url")
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, resolvedURL)
	if err != nil {
		h.setCachedCover(cacheKey, "", false, 2*time.Minute)
		return "", fmt.Errorf("resolve source url: %w", err)
	}

	coverURL := ""
	if result != nil {
		coverURL = strings.TrimSpace(result.CoverImageURL)
	}
	if coverURL == "" {
		h.setCachedCover(cacheKey, "", false, 30*time.Minute)
		return "", fmt.Errorf("cover not found")
	}

	h.setCachedCover(cacheKey, coverURL, true, 12*time.Hour)
	return coverURL, nil
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
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"chapterInputValue": chapterInputValue,
		"textInputValue":    textInputValue,
		"timeInputValue":    timeInputValue,
		"toJSON":            toJSON,
		"statusLabel":       statusLabel,
		"sortLabel":         sortLabel,
	}).ParseGlob("web/templates/*.html")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Template load error")
	}
	c.Type("html", "utf-8")
	return tmpl.ExecuteTemplate(c.Response().BodyWriter(), templateName, data)
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
