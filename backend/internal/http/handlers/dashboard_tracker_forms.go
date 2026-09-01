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
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
	"github.com/gabriel/cross-site-tracker/backend/internal/sourcepick"
	"github.com/gofiber/fiber/v2"
)

func (h *DashboardHandler) NewTrackerModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	viewMode := normalizeViewMode(c.Query("view", "grid"))

	sources, err := h.sourceRepo.ListEnabled(c.Context())
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load sources", err)
	}

	profileTags, err := h.trackerRepo.ListProfileTags(c.Context(), activeProfile.ID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load profile tags", err)
	}

	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:          "create",
		ViewMode:      viewMode,
		Sources:       sources,
		LinkedSources: []models.TrackerSource{},
		ProfileTags:   profileTags,
		TrackerTags:   []models.CustomTag{},
	})
}

func (h *DashboardHandler) EmptyModal(c *fiber.Ctx) error {
	return h.render(c, "empty_modal.html", nil)
}

func (h *DashboardHandler) EditTrackerModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	viewMode := normalizeViewMode(c.Query("view", "grid"))

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	tracker, err := h.trackerRepo.GetByID(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load tracker", err)
	}
	if tracker == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	sources, err := h.sourceRepo.ListEnabled(c.Context())
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load sources", err)
	}

	linkedSources, err := h.trackerRepo.ListTrackerSources(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load linked sources", err)
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

	profileTags, err := h.trackerRepo.ListProfileTags(c.Context(), activeProfile.ID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load profile tags", err)
	}

	readingOptions := make([]models.TrackerSource, 0, len(linkedSources))
	seenReadingSources := map[int64]bool{}
	for _, linked := range linkedSources {
		if linked.SourceID <= 0 || seenReadingSources[linked.SourceID] {
			continue
		}
		seenReadingSources[linked.SourceID] = true
		readingOptions = append(readingOptions, linked)
	}
	readingSourceID := int64(0)
	if tracker.ReadingSourceID != nil {
		readingSourceID = *tracker.ReadingSourceID
	}

	return h.render(c, "tracker_form_modal.html", trackerFormData{
		Mode:            "edit",
		ViewMode:        viewMode,
		Tracker:         tracker,
		Sources:         sources,
		LinkedSources:   linkedSources,
		ReadingOptions:  readingOptions,
		ReadingSourceID: readingSourceID,
		ProfileTags:     profileTags,
		TrackerTags:     tracker.Tags,
	})
}

func (h *DashboardHandler) CreateFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	tracker, err := parseTrackerFromForm(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}
	tracker.ProfileID = activeProfile.ID

	// Everything the form carries is checked before the first write. The tag
	// selection and the pasted link used to be parsed after the INSERT, so a bad
	// value answered 400 with the tracker already created — and the natural
	// retry created it twice.
	tagIDs, err := parseTagIDsFromForm(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}
	pastedLink, err := h.parsePastedLink(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}

	h.enrichTrackerFromSource(c.Context(), tracker)

	now := time.Now().UTC()
	tracker.LastCheckedAt = &now

	exists, err := h.trackerRepo.SourceExists(c.Context(), tracker.SourceID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to validate source", err)
	}
	if !exists {
		return h.fail(c, fiber.StatusBadRequest, "Selected source does not exist", nil)
	}

	created, err := h.trackerRepo.Create(c.Context(), tracker)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to create tracker", err)
	}
	if created == nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	if err := h.trackerRepo.ReplaceTrackerTags(c.Context(), activeProfile.ID, created.ID, tagIDs); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to save tracker tags", err)
	}
	if pastedLink != nil {
		if err := h.trackerRepo.UpsertTrackerSource(c.Context(), activeProfile.ID, created.ID, *pastedLink); err != nil {
			return h.fail(c, fiber.StatusInternalServerError, "Failed to save the pasted link", err)
		}
		h.invalidateLinkLookups()
	}

	c.Set("HX-Trigger", fmt.Sprintf(`{"trackerCreated":{"id":%d}}`, created.ID))
	return h.render(c, "empty_modal.html", nil)
}

// parsePastedLink resolves the optional "link another site by URL" field into
// the row to store, without storing it. It runs before any write so a URL no
// connector claims refuses the whole save — the same answer on create and on
// edit, where the two used to differ (silently dropped on create, a 400 after
// the tracker was already rewritten on edit). Nil means the field was empty.
func (h *DashboardHandler) parsePastedLink(c *fiber.Ctx) (*models.TrackerSource, error) {
	linkedURL := strings.TrimSpace(c.FormValue("linked_url"))
	if linkedURL == "" {
		return nil, nil
	}
	_, link, err := h.resolveSourceLink(c.Context(), linkedURL)
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Could not link the pasted URL: "+err.Error())
	}
	return &link, nil
}

type trackerCardFragmentData struct {
	ViewMode string
	Card     trackerCardView
}

func (h *DashboardHandler) CardFragment(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	viewMode := normalizeViewMode(c.Query("view", "grid"))

	// Unlike the mutation handlers, this one must keep failing loudly: the
	// pinned-card script fetches it and retries on a non-OK response, so a
	// degraded 200 would end that retry loop with an empty card.
	card, err := h.loadTrackerCardView(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load tracker card", err)
	}
	if card == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	return h.render(c, "tracker_card_fragment.html", trackerCardFragmentData{
		ViewMode: viewMode,
		Card:     *card,
	})
}

func (h *DashboardHandler) enrichTrackerFromSource(parent context.Context, tracker *models.Tracker) {
	if tracker == nil || strings.TrimSpace(tracker.SourceURL) == "" || tracker.SourceID <= 0 {
		return
	}
	if hasResolvedSourceMetadata(tracker) {
		return
	}

	source, err := h.sourceRepo.GetByID(parent, tracker.SourceID)
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
	if resolvedURL := strings.TrimSpace(resolved.URL); resolvedURL != "" {
		tracker.SourceURL = resolvedURL
	}

	if (tracker.LatestKnownChapter == nil || *tracker.LatestKnownChapter <= 0) && resolved.LatestChapter != nil {
		tracker.LatestKnownChapter = resolved.LatestChapter
	}

	if resolved.LastUpdatedAt != nil {
		updatedAt := resolved.LastUpdatedAt.UTC()
		tracker.LatestReleaseAt = &updatedAt
	}
	if len(resolved.RelatedTitles) > 0 {
		tracker.RelatedTitles = searchutil.FilterEnglishAlphabetNames(resolved.RelatedTitles)
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
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	viewMode := normalizeViewMode(c.FormValue("view_mode", c.Query("view", "grid")))

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	existingTracker, err := h.trackerRepo.GetByID(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load tracker", err)
	}
	if existingTracker == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	tracker, err := parseTrackerFromForm(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}
	tracker.LastCheckedAt = existingTracker.LastCheckedAt
	tracker.LatestReleaseAt = existingTracker.LatestReleaseAt
	tracker.Rating = existingTracker.Rating
	tracker.ProfileID = activeProfile.ID

	linkedSources, err := parseLinkedSourcesFromForm(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}

	// Parsed up front, like on create: a save is all-or-nothing as far as the
	// form's own values go. These three used to be read after the UPDATE and
	// the linked-sources replace had committed, so an invalid tag id or a URL
	// no connector claims left the title and sources saved, the tags and the
	// reading pin not, and answered 400.
	tagIDs, err := parseTagIDsFromForm(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}
	readingSourceID, err := parseReadingSourceFromForm(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}
	pastedLink, err := h.parsePastedLink(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
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
		exists, err := h.trackerRepo.SourceExists(c.Context(), source.SourceID)
		if err != nil {
			return h.fail(c, fiber.StatusInternalServerError, "Failed to validate linked source", err)
		}
		if !exists {
			return h.fail(c, fiber.StatusBadRequest, "One of the linked sources does not exist", nil)
		}
	}

	existingSources, err := h.trackerRepo.ListTrackerSources(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load linked sources", err)
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
		primarySource, latestKnownChapter, latestReleaseAt, relatedTitles := h.selectPrimaryTrackerSource(c.Context(), uniqueSources)
		tracker.SourceID = primarySource.SourceID
		tracker.SourceItemID = primarySource.SourceItemID
		tracker.SourceURL = primarySource.SourceURL
		if latestKnownChapter != nil {
			tracker.LatestKnownChapter = latestKnownChapter
		}
		if latestReleaseAt != nil {
			tracker.LatestReleaseAt = latestReleaseAt
		}
		if len(relatedTitles) > 0 {
			tracker.RelatedTitles = relatedTitles
		}
	}

	exists, err := h.trackerRepo.SourceExists(c.Context(), tracker.SourceID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to validate source", err)
	}
	if !exists {
		return h.fail(c, fiber.StatusBadRequest, "Selected source does not exist", nil)
	}

	updated, err := h.trackerRepo.Update(c.Context(), activeProfile.ID, id, tracker)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to update tracker", err)
	}
	if updated == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	if err := h.trackerRepo.ReplaceTrackerSources(c.Context(), activeProfile.ID, id, uniqueSources); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to save linked sources", err)
	}

	if err := h.trackerRepo.ReplaceTrackerTags(c.Context(), activeProfile.ID, id, tagIDs); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to save tracker tags", err)
	}

	// After ReplaceTrackerSources, so the freshly pasted link survives the
	// replace instead of being wiped by it.
	if pastedLink != nil {
		if err := h.trackerRepo.UpsertTrackerSource(c.Context(), activeProfile.ID, id, *pastedLink); err != nil {
			return h.fail(c, fiber.StatusInternalServerError, "Failed to save the pasted link", err)
		}
	}

	if err := h.trackerRepo.SetReadingSource(c.Context(), activeProfile.ID, id, readingSourceID); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to save reading site", err)
	}

	// The saved links or pin may differ from what the cached chapter URLs
	// were computed against.
	h.invalidateLinkLookups()

	return h.renderSingleCardOOB(c, activeProfile.ID, id, viewMode)
}

func (h *DashboardHandler) DeleteFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	deleted, err := h.trackerRepo.Delete(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to delete tracker", err)
	}
	if !deleted {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{DeleteTrackerID: id})
}

func (h *DashboardHandler) SetLastReadFromCard(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	viewMode := normalizeViewMode(c.FormValue("view_mode", c.Query("view", "grid")))

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	tracker, err := h.trackerRepo.GetByID(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load tracker", err)
	}
	if tracker == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	if tracker.LatestKnownChapter != nil {
		_, err := h.trackerRepo.UpdateLastReadChapter(c.Context(), activeProfile.ID, id, tracker.LatestKnownChapter)
		if err != nil {
			return h.fail(c, fiber.StatusInternalServerError, "Failed to update tracker", err)
		}
	}

	return h.renderSingleCardOOB(c, activeProfile.ID, id, viewMode)
}

func (h *DashboardHandler) SetRatingFromCard(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	viewMode := normalizeViewMode(c.FormValue("view_mode", c.Query("view", "grid")))

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	tracker, err := h.trackerRepo.GetByID(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load tracker", err)
	}
	if tracker == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	var rating *float64
	if strings.TrimSpace(c.FormValue("clear")) != "1" {
		raw := strings.TrimSpace(c.FormValue("rating"))
		if raw == "" {
			return h.fail(c, fiber.StatusBadRequest, "Rating is required", nil)
		}

		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			return h.fail(c, fiber.StatusBadRequest, "Invalid rating", parseErr)
		}
		rating = &value
	}

	if err := validateTrackerRating(rating); err != nil {
		return h.fail(c, fiber.StatusBadRequest, err.Error(), err)
	}

	if _, err := h.trackerRepo.UpdateRating(c.Context(), activeProfile.ID, id, rating); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to update rating", err)
	}

	return h.renderSingleCardOOB(c, activeProfile.ID, id, viewMode)
}

// loadTrackerCardView rebuilds one tracker's card the way the list builds it,
// so a single-card re-render cannot drift from the list render. A nil card with
// a nil error means the tracker is gone (or no longer produces a card), which
// callers must be able to tell apart from a query that failed: one is a 404,
// the other a 500.
func (h *DashboardHandler) loadTrackerCardView(ctx context.Context, profileID int64, trackerID int64) (*trackerCardView, error) {
	tracker, err := h.trackerRepo.GetByID(ctx, profileID, trackerID)
	if err != nil {
		return nil, err
	}
	if tracker == nil {
		return nil, nil
	}

	sourceByID, err := h.listSourcesByID(ctx)
	if err != nil {
		return nil, err
	}

	sourceLogoBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(ctx, profileID)
	if err != nil {
		return nil, err
	}

	cards, _ := h.buildTrackerCards(
		[]models.Tracker{*tracker},
		sourceByID,
		sourceLogoBySourceID,
		h.trackerAlternatesForProfile(ctx, profileID),
		"",
	)
	if len(cards) == 0 {
		return nil, nil
	}

	return &cards[0], nil
}

// renderSingleCardOOB answers a mutation by swapping just that tracker's card.
// The write has already committed when this runs, so a card that cannot be
// rebuilt degrades to a trackersChanged trigger instead of an error status: the
// browser reloads the list and sees the saved state, rather than being told a
// save failed that in fact succeeded.
func (h *DashboardHandler) renderSingleCardOOB(c *fiber.Ctx, profileID int64, trackerID int64, viewMode string) error {
	card, err := h.loadTrackerCardView(c.Context(), profileID, trackerID)
	if err != nil || card == nil {
		c.Set("HX-Trigger", `{"trackersChanged":true}`)
		return h.render(c, "empty_modal.html", nil)
	}

	return h.render(c, "tracker_oob_response.html", trackerOOBResponseData{
		ViewMode:    viewMode,
		ReplaceCard: card,
	})
}

func (h *DashboardHandler) listSourcesByID(ctx context.Context) (map[int64]models.Source, error) {
	sources, err := h.sourceRepo.ListEnabled(ctx)
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
	// These three are written for the reader and are shown verbatim in the
	// form, so they keep their sentence capitalisation. fiber.Error carries a
	// message meant for a response — its Error() returns exactly the text
	// below — which is what publicRequestMessage looks for when deciding
	// whether an error is safe to display.
	title := strings.TrimSpace(c.FormValue("title"))
	if title == "" {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Title is required")
	}

	sourceID, err := strconv.ParseInt(strings.TrimSpace(c.FormValue("source_id")), 10, 64)
	if err != nil || sourceID <= 0 {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Valid source is required")
	}

	sourceURL, err := validateSourceURL(c.FormValue("source_url"))
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Source URL must be an absolute http(s) address")
	}

	status := strings.TrimSpace(c.FormValue("status"))
	if status == "" {
		status = "reading"
	}
	// The same closed set the JSON API enforces. A junk value used to reach the
	// database's CHECK constraint and come back as a 500, or — before that
	// constraint — be stored, styled as an unknown badge and listed under no
	// status filter.
	status, err = validateTrackerStatus(status)
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid status")
	}

	var sourceItemID *string
	if raw := strings.TrimSpace(c.FormValue("source_item_id")); raw != "" {
		sourceItemID = &raw
	}

	lastRead, err := parseOptionalFloat(c.FormValue("last_read_chapter"))
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid last read chapter")
	}
	latestKnown, err := parseOptionalFloat(c.FormValue("latest_known_chapter"))
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid latest known chapter")
	}

	latestReleaseAt, err := parseOptionalRFC3339Time(c.FormValue("latest_release_at"))
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid latest release date")
	}
	relatedTitles, err := parseRelatedTitlesFromForm(c.FormValue("related_titles_json"))
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid related titles")
	}

	return &models.Tracker{
		Title:              title,
		RelatedTitles:      relatedTitles,
		SourceID:           sourceID,
		SourceItemID:       sourceItemID,
		SourceURL:          sourceURL,
		Status:             status,
		LastReadChapter:    lastRead,
		LatestKnownChapter: latestKnown,
		LatestReleaseAt:    latestReleaseAt,
	}, nil
}

// parseReadingSourceFromForm reads the reading-site pin: empty/0/"auto" mean
// auto (nil).
func parseReadingSourceFromForm(c *fiber.Ctx) (*int64, error) {
	raw := strings.TrimSpace(c.FormValue("reading_source_id"))
	if raw == "" || raw == "0" || strings.EqualFold(raw, "auto") {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid reading site")
	}
	return &id, nil
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

func parseRelatedTitlesFromForm(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil, err
	}

	return searchutil.FilterEnglishAlphabetNames(values), nil
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
			return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid tag selection")
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
		return nil, fiber.NewError(fiber.StatusBadRequest, "Invalid linked sources payload")
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

// selectPrimaryTrackerSource resolves every linked source and picks the one
// whose reading is best, by the shared rule the poller ranks its fallback
// sources with (internal/sourcepick) — the two used to be near-copies that
// disagreed on the equal-chapter tie-break.
//
// It also canonicalizes each source it managed to resolve, in place: the
// caller stores the whole list, so a source that loses the comparison still
// gets its item id filled in and its URL rewritten to the site's own.
//
// The first source stays the primary when nothing resolves, which is what
// makes an unreachable site (MangaFire behind its challenge) survive a save
// instead of being demoted by an outage.
func (h *DashboardHandler) selectPrimaryTrackerSource(parent context.Context, sources []models.TrackerSource) (models.TrackerSource, *float64, *time.Time, []string) {
	if len(sources) == 0 {
		return models.TrackerSource{}, nil, nil, nil
	}

	bestIndex := 0
	best := sourcepick.Reading{}
	var bestRelatedTitles []string

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
		if resolvedURL := strings.TrimSpace(resolved.URL); resolvedURL != "" {
			source.SourceURL = resolvedURL
		}
		resolvedRelatedTitles := searchutil.FilterEnglishAlphabetNames(resolved.RelatedTitles)
		if idx == bestIndex && len(bestRelatedTitles) == 0 && len(resolvedRelatedTitles) > 0 {
			bestRelatedTitles = resolvedRelatedTitles
		}

		// Copied out of the connector's result: what is returned here is stored
		// on the tracker and must not alias a value the connector still owns.
		candidate := sourcepick.Reading{}
		if resolved.LatestChapter != nil {
			resolvedChapter := *resolved.LatestChapter
			candidate.Chapter = &resolvedChapter
		}
		if resolved.LastUpdatedAt != nil {
			resolvedReleaseAt := resolved.LastUpdatedAt.UTC()
			candidate.ReleaseAt = &resolvedReleaseAt
		}

		if sourcepick.Better(best, candidate) {
			bestIndex = idx
			best = candidate
			bestRelatedTitles = resolvedRelatedTitles
		}
	}

	return sources[bestIndex], best.Chapter, best.ReleaseAt, bestRelatedTitles
}

func (h *DashboardHandler) resolveLinkedSource(parent context.Context, sourceID int64, sourceURL string) (*connectors.MangaResult, error) {
	if sourceID <= 0 || strings.TrimSpace(sourceURL) == "" {
		return nil, fmt.Errorf("source is incomplete")
	}

	source, err := h.sourceRepo.GetByID(parent, sourceID)
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
