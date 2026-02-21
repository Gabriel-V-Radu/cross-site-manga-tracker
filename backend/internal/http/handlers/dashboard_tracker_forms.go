package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gofiber/fiber/v2"
)

func (h *DashboardHandler) NewTrackerModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}
	viewMode := normalizeViewMode(c.Query("view", "grid"))

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
		ViewMode:      viewMode,
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

func (h *DashboardHandler) EditTrackerModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}
	viewMode := normalizeViewMode(c.Query("view", "grid"))

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
		ViewMode:      viewMode,
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
	if created == nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	c.Set("HX-Trigger", fmt.Sprintf(`{"trackerCreated":{"id":%d}}`, created.ID))
	return h.render(c, "empty_modal.html", nil)
}

type trackerCardFragmentData struct {
	ViewMode string
	Card     trackerCardView
}

func (h *DashboardHandler) CardFragment(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tracker id")
	}

	viewMode := normalizeViewMode(c.Query("view", "grid"))

	tracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load tracker")
	}
	if tracker == nil {
		return c.Status(fiber.StatusNotFound).SendString("Tracker not found")
	}

	sourceByID, err := h.listSourcesByID()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked site logos")
	}

	cards, _ := h.buildTrackerCards([]models.Tracker{*tracker}, sourceByID, sourceLogoBySourceID, "")
	if len(cards) == 0 {
		return c.Status(fiber.StatusNotFound).SendString("Tracker card not found")
	}

	return h.render(c, "tracker_card_fragment.html", trackerCardFragmentData{
		ViewMode: viewMode,
		Card:     cards[0],
	})
}

func (h *DashboardHandler) enrichTrackerFromSource(parent context.Context, tracker *models.Tracker) {
	if tracker == nil || strings.TrimSpace(tracker.SourceURL) == "" || tracker.SourceID <= 0 {
		return
	}
	if hasResolvedSourceMetadata(tracker) {
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

	if (tracker.LatestKnownChapter == nil || *tracker.LatestKnownChapter <= 0) && resolved.LatestChapter != nil {
		tracker.LatestKnownChapter = resolved.LatestChapter
	}

	if resolved.LastUpdatedAt != nil {
		updatedAt := resolved.LastUpdatedAt.UTC()
		tracker.LatestReleaseAt = &updatedAt
	}
}

func hasResolvedSourceMetadata(tracker *models.Tracker) bool {
	if tracker == nil {
		return false
	}

	if tracker.SourceItemID == nil || strings.TrimSpace(*tracker.SourceItemID) == "" {
		return false
	}
	if tracker.LatestKnownChapter == nil || *tracker.LatestKnownChapter <= 0 {
		return false
	}

	// Require release date so add/edit flows do not display "released just now"
	// when the source can provide an older chapter timestamp.
	if tracker.LatestReleaseAt == nil {
		return false
	}

	return true
}

func (h *DashboardHandler) UpdateFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	viewMode := normalizeViewMode(c.FormValue("view_mode", c.Query("view", "grid")))

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
	tracker.Rating = existingTracker.Rating
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

	existingSources, err := h.trackerRepo.ListTrackerSources(activeProfile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sources")
	}
	if len(existingSources) == 0 {
		existingSources = []models.TrackerSource{
			{
				TrackerID:    id,
				SourceID:     existingTracker.SourceID,
				SourceItemID: existingTracker.SourceItemID,
				SourceURL:    existingTracker.SourceURL,
			},
		}
	}

	if !sameTrackerSources(existingSources, uniqueSources) {
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

	sourceByID, err := h.listSourcesByID()
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	cards, _ := h.buildTrackerCards([]models.Tracker{*fullTracker}, sourceByID, sourceLogoBySourceID, "")
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

	sourceByID, err := h.listSourcesByID()
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	cards, _ := h.buildTrackerCards([]models.Tracker{*updatedTracker}, sourceByID, sourceLogoBySourceID, "")
	if len(cards) == 0 {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{
		ViewMode:    viewMode,
		ReplaceCard: &cards[0],
	})
}

func (h *DashboardHandler) SetRatingFromCard(c *fiber.Ctx) error {
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

	var rating *float64
	if strings.TrimSpace(c.FormValue("clear")) != "1" {
		raw := strings.TrimSpace(c.FormValue("rating"))
		if raw == "" {
			return c.Status(fiber.StatusBadRequest).SendString("Rating is required")
		}

		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid rating")
		}
		rating = &value
	}

	if err := validateTrackerRating(rating); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	if _, err := h.trackerRepo.UpdateRating(activeProfile.ID, id, rating); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to update rating")
	}

	updatedTracker, err := h.trackerRepo.GetByID(activeProfile.ID, id)
	if err != nil || updatedTracker == nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	sourceByID, err := h.listSourcesByID()
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	cards, _ := h.buildTrackerCards([]models.Tracker{*updatedTracker}, sourceByID, sourceLogoBySourceID, "")
	if len(cards) == 0 {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{
		ViewMode:    viewMode,
		ReplaceCard: &cards[0],
	})
}

func (h *DashboardHandler) listSourcesByID() (map[int64]models.Source, error) {
	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return nil, err
	}

	sourceByID := make(map[int64]models.Source, len(sources))
	for _, source := range sources {
		sourceByID[source.ID] = source
	}

	return sourceByID, nil
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

func sameTrackerSources(existing []models.TrackerSource, incoming []models.TrackerSource) bool {
	if len(existing) != len(incoming) {
		return false
	}

	makeKey := func(item models.TrackerSource) string {
		sourceItemID := ""
		if item.SourceItemID != nil {
			sourceItemID = strings.ToLower(strings.TrimSpace(*item.SourceItemID))
		}
		return fmt.Sprintf(
			"%d|%s|%s",
			item.SourceID,
			strings.ToLower(strings.TrimSpace(item.SourceURL)),
			sourceItemID,
		)
	}

	existingSet := make(map[string]int, len(existing))
	for _, item := range existing {
		key := makeKey(item)
		existingSet[key]++
	}

	for _, item := range incoming {
		key := makeKey(item)
		if existingSet[key] == 0 {
			return false
		}
		existingSet[key]--
	}

	for _, remaining := range existingSet {
		if remaining != 0 {
			return false
		}
	}

	return true
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
