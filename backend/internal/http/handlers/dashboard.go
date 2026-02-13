package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
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
	trackerRepo *repository.TrackerRepository
	sourceRepo  *repository.SourceRepository
	registry    *connectors.Registry
	httpClient  *http.Client
	coverCache  map[string]coverCacheEntry
	cacheMu     sync.RWMutex
}

type coverCacheEntry struct {
	CoverURL  string
	Found     bool
	ExpiresAt time.Time
}

type dashboardPageData struct {
	Statuses []string
	Sorts    []string
}

type trackersPartialData struct {
	Trackers []trackerCardView
	ViewMode string
}

type trackerCardView struct {
	ID                    int64
	Title                 string
	Status                string
	SourceURL             string
	CoverURL              string
	LatestKnownChapter    string
	LastCheckedAgo        string
	LastReadChapter       string
	LastReadAgo           string
	UpdatedAtFormatted    string
	LastCheckedFormatted  string
	SourceItemID          *string
	LatestKnownChapterRaw *float64
	LastReadChapterRaw    *float64
}

type trackerFormData struct {
	Mode                 string
	Tracker              *models.Tracker
	Sources              []models.Source
	AvailableLinkSources []models.Source
	LinkedSources        []models.TrackerSource
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
		trackerRepo: repository.NewTrackerRepository(db),
		sourceRepo:  repository.NewSourceRepository(db),
		registry:    registry,
		httpClient:  &http.Client{Timeout: 8 * time.Second},
		coverCache:  make(map[string]coverCacheEntry),
	}
}

func (h *DashboardHandler) Page(c *fiber.Ctx) error {
	data := dashboardPageData{
		Statuses: []string{"all", "reading", "completed", "on_hold", "dropped", "plan_to_read"},
		Sorts:    []string{"updated_at", "title", "created_at", "last_checked_at", "latest_known_chapter"},
	}
	return h.render(c, "dashboard_page.html", data)
}

func (h *DashboardHandler) TrackersPartial(c *fiber.Ctx) error {
	status := strings.TrimSpace(c.Query("status", "all"))
	statuses := make([]string, 0)
	if status != "" && status != "all" {
		statuses = append(statuses, status)
	}

	viewMode := strings.TrimSpace(c.Query("view", "grid"))
	if viewMode != "grid" && viewMode != "list" {
		viewMode = "grid"
	}

	items, err := h.trackerRepo.List(repository.TrackerListOptions{
		Statuses: statuses,
		SortBy:   strings.TrimSpace(c.Query("sort", "updated_at")),
		Order:    strings.TrimSpace(c.Query("order", "desc")),
		Query:    strings.TrimSpace(c.Query("q")),
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
			ID:                    item.ID,
			Title:                 item.Title,
			Status:                item.Status,
			SourceURL:             item.SourceURL,
			SourceItemID:          item.SourceItemID,
			LatestKnownChapterRaw: item.LatestKnownChapter,
			LastReadChapterRaw:    item.LastReadChapter,
			UpdatedAtFormatted:    item.UpdatedAt.Format("2006-01-02 15:04"),
			LastReadAgo:           relativeTime(item.UpdatedAt),
		}

		if item.LastCheckedAt != nil {
			card.LastCheckedFormatted = item.LastCheckedAt.Format("2006-01-02 15:04")
			card.LastCheckedAgo = relativeTime(*item.LastCheckedAt)
		} else {
			card.LastCheckedFormatted = "—"
			card.LastCheckedAgo = "—"
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
		if sourceKey == "mangadex" && item.SourceItemID != nil && strings.TrimSpace(*item.SourceItemID) != "" {
			if coverURL, coverErr := h.fetchMangaDexCoverURL(c.Context(), *item.SourceItemID); coverErr == nil {
				card.CoverURL = coverURL
			}
		}

		cards = append(cards, card)
	}

	return h.render(c, "trackers_partial.html", trackersPartialData{Trackers: cards, ViewMode: viewMode})
}

func (h *DashboardHandler) NewTrackerModal(c *fiber.Ctx) error {
	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}
	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:                 "create",
		Sources:              sources,
		AvailableLinkSources: sources,
		LinkedSources:        []models.TrackerSource{},
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
	if source.Key == "mangaplus" {
		searchTimeout = 12 * time.Second
	}

	ctx, cancel := context.WithTimeout(c.Context(), searchTimeout)
	defer cancel()

	results, err := connector.SearchByTitle(ctx, query, 8)
	if err != nil {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Search failed for this source: " + err.Error(), SourceID: source.ID, SourceName: source.Name, Intent: intent})
	}

	return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Items: results, Query: query, SourceID: source.ID, SourceName: source.Name, Intent: intent})
}

func (h *DashboardHandler) EditTrackerModal(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	tracker, err := h.trackerRepo.GetByID(id)
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

	linkedSources, err := h.trackerRepo.ListTrackerSources(id)
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
		Mode:                 "edit",
		Tracker:              tracker,
		Sources:              sources,
		AvailableLinkSources: filterAvailableLinkSources(sources, linkedSources),
		LinkedSources:        linkedSources,
	})
}

func filterAvailableLinkSources(sources []models.Source, linkedSources []models.TrackerSource) []models.Source {
	linkedBySourceID := make(map[int64]struct{}, len(linkedSources))
	for _, linked := range linkedSources {
		linkedBySourceID[linked.SourceID] = struct{}{}
	}

	filtered := make([]models.Source, 0, len(sources))
	for _, source := range sources {
		if _, exists := linkedBySourceID[source.ID]; exists {
			continue
		}
		filtered = append(filtered, source)
	}

	return filtered
}

func (h *DashboardHandler) CreateFromForm(c *fiber.Ctx) error {
	tracker, err := parseTrackerFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

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

func (h *DashboardHandler) UpdateFromForm(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	tracker, err := parseTrackerFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	exists, err := h.trackerRepo.SourceExists(tracker.SourceID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to validate source")
	}
	if !exists {
		return c.Status(fiber.StatusBadRequest).SendString("Selected source does not exist")
	}

	updated, err := h.trackerRepo.Update(id, tracker)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to update tracker")
	}
	if updated == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	linkedSources, err := parseLinkedSourcesFromForm(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	mergedSources := make([]models.TrackerSource, 0, len(linkedSources)+1)
	mergedSources = append(mergedSources, models.TrackerSource{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	})
	mergedSources = append(mergedSources, linkedSources...)

	uniqueSources := dedupeTrackerSources(mergedSources)
	for _, source := range uniqueSources {
		exists, err := h.trackerRepo.SourceExists(source.SourceID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to validate linked source")
		}
		if !exists {
			return c.Status(fiber.StatusBadRequest).SendString("One of the linked sources does not exist")
		}
	}

	if err := h.trackerRepo.ReplaceTrackerSources(id, uniqueSources); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save linked sources")
	}

	c.Set("HX-Trigger", `{"trackersChanged":true}`)
	return h.render(c, "empty_modal.html", nil)
}

func (h *DashboardHandler) DeleteFromForm(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	deleted, err := h.trackerRepo.Delete(id)
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
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	tracker, err := h.trackerRepo.GetByID(id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load tracker")
	}
	if tracker == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	if tracker.LatestKnownChapter != nil {
		tracker.LastReadChapter = tracker.LatestKnownChapter
		now := time.Now().UTC()
		tracker.LastCheckedAt = &now
		if _, err := h.trackerRepo.Update(id, tracker); err != nil {
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

	now := time.Now().UTC()
	return &models.Tracker{
		Title:              title,
		SourceID:           sourceID,
		SourceItemID:       sourceItemID,
		SourceURL:          sourceURL,
		Status:             status,
		LastReadChapter:    lastRead,
		LatestKnownChapter: latestKnown,
		LastCheckedAt:      &now,
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

func (h *DashboardHandler) fetchMangaDexCoverURL(parent context.Context, titleID string) (string, error) {
	trimmedID := strings.TrimSpace(titleID)
	if trimmedID == "" {
		return "", fmt.Errorf("empty title id")
	}

	if cachedURL, found, ok := h.getCachedCover(trimmedID); ok {
		if found {
			return cachedURL, nil
		}
		return "", fmt.Errorf("cover not found")
	}

	endpoint := "https://api.mangadex.org/manga/" + url.PathEscape(trimmedID) + "?includes[]=cover_art"
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build cover request: %w", err)
	}

	res, err := h.httpClient.Do(req)
	if err != nil {
		h.setCachedCover(trimmedID, "", false, 2*time.Minute)
		return "", fmt.Errorf("cover request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		h.setCachedCover(trimmedID, "", false, 10*time.Minute)
		return "", fmt.Errorf("cover request status: %d", res.StatusCode)
	}

	var payload struct {
		Data struct {
			Relationships []struct {
				Type       string `json:"type"`
				Attributes struct {
					FileName string `json:"fileName"`
				} `json:"attributes"`
			} `json:"relationships"`
		} `json:"data"`
	}

	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		h.setCachedCover(trimmedID, "", false, 2*time.Minute)
		return "", fmt.Errorf("decode cover response: %w", err)
	}

	for _, relation := range payload.Data.Relationships {
		if relation.Type == "cover_art" {
			fileName := strings.TrimSpace(relation.Attributes.FileName)
			if fileName != "" {
				coverURL := "https://uploads.mangadex.org/covers/" + trimmedID + "/" + fileName + ".512.jpg"
				h.setCachedCover(trimmedID, coverURL, true, 12*time.Hour)
				return coverURL, nil
			}
		}
	}

	h.setCachedCover(trimmedID, "", false, 30*time.Minute)

	return "", fmt.Errorf("cover not found")
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
		"toJSON":            toJSON,
	}).ParseGlob("web/templates/*.html")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Template load error")
	}
	c.Type("html", "utf-8")
	return tmpl.ExecuteTemplate(c.Response().BodyWriter(), templateName, data)
}

func toJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
