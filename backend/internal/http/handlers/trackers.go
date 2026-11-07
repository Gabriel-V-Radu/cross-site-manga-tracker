package handlers

import (
	"database/sql"
	"fmt"
	"mime"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
	"github.com/gofiber/fiber/v2"
)

type createTrackerRequest struct {
	Title              string   `json:"title"`
	RelatedTitles      []string `json:"relatedTitles"`
	SourceID           int64    `json:"sourceId"`
	SourceItemID       *string  `json:"sourceItemId"`
	SourceURL          string   `json:"sourceUrl"`
	Status             string   `json:"status"`
	LastReadChapter    *float64 `json:"lastReadChapter"`
	Rating             *float64 `json:"rating"`
	LatestKnownChapter *float64 `json:"latestKnownChapter"`
	LastCheckedAt      *string  `json:"lastCheckedAt"`
}

type updateTrackerRequest = createTrackerRequest

type TrackersHandler struct {
	repo            *repository.TrackerRepository
	sourceRepo      *repository.SourceRepository
	registry        *connectors.Registry
	profileResolver *profileContextResolver
}

// NewTrackersHandler takes the connector registry to tell which site a
// tracker's URL belongs to; a nil registry trusts the sourceId as sent.
func NewTrackersHandler(db *sql.DB, registry *connectors.Registry) *TrackersHandler {
	return &TrackersHandler{
		repo:            repository.NewTrackerRepository(db),
		sourceRepo:      repository.NewSourceRepository(db),
		registry:        registry,
		profileResolver: newProfileContextResolver(db),
	}
}

// parseTrackerBody decodes a create or update body, answering for the request
// itself when it cannot (the bool is false and the response is written). The
// body must be declared as JSON: fiber's BodyParser picks its decoder from the
// Content-Type, so a JSON body sent with none — or with the form type that
// curl -d and Python's urllib add by default — was decoded as an empty form
// and refused with "title is required", about a body that plainly had one.
// Requiring the header also means a page on another site cannot send such a
// body without a CORS preflight, which this server never grants.
func parseTrackerBody(c *fiber.Ctx, req *createTrackerRequest) (bool, error) {
	if !isJSONContentType(string(c.Request().Header.ContentType())) {
		return false, c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{"message": "request body must be JSON sent with Content-Type: application/json"})
	}
	if err := c.BodyParser(req); err != nil {
		return false, c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid json body"})
	}
	return true, nil
}

// isJSONContentType accepts application/json, with or without parameters, and
// the structured "+json" types BodyParser also decodes as JSON.
func isJSONContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	return mediaType == fiber.MIMEApplicationJSON || strings.HasSuffix(mediaType, "+json")
}

// alignSourceWithURL stores the source the tracker's URL belongs to, the rule
// the dashboard form follows: sourceId 4 (Asura) sent with a MangaDex link was
// stored as an Asura tracker, which no poll can read. It then checks the
// source exists. A false return means the response is written.
func (h *TrackersHandler) alignSourceWithURL(c *fiber.Ctx, tracker *models.Tracker) (bool, error) {
	sourceID, err := sourceIDOwningURL(c.Context(), h.registry, h.sourceRepo, tracker.SourceID, tracker.SourceURL)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to validate source", err)
		return false, c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to validate source"})
	}
	tracker.SourceID = sourceID

	exists, err := h.repo.SourceExists(c.Context(), tracker.SourceID)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to validate source", err)
		return false, c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to validate source"})
	}
	if !exists {
		return false, c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "sourceId does not exist"})
	}
	return true, nil
}

// failProfileJSON is the API's failProfile: an unknown profile is the
// client's mistake and keeps its 400 with the same message as before, while a
// lookup that failed is the database failing — a logged 500 that does not
// hand the SQLite error text to the client.
func failProfileJSON(c *fiber.Ctx, err error) error {
	if isUnknownProfile(err) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}
	logHandlerFailure(c, fiber.StatusInternalServerError, "failed to resolve profile", err)
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to load profile"})
}

func (h *TrackersHandler) Create(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return failProfileJSON(c, err)
	}

	var req createTrackerRequest
	if ok, err := parseTrackerBody(c, &req); !ok {
		return err
	}

	tracker, err := validateAndBuildTracker(req, nil)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	if ok, err := h.alignSourceWithURL(c, tracker); !ok {
		return err
	}

	tracker.ProfileID = profile.ID

	created, err := h.repo.Create(c.Context(), tracker)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to create tracker", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to create tracker"})
	}

	return c.Status(fiber.StatusCreated).JSON(created)
}

func (h *TrackersHandler) List(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return failProfileJSON(c, err)
	}

	statuses := parseStatuses(c.Query("status"))
	if err := validateStatuses(statuses); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	options := repository.TrackerListOptions{
		ProfileID: profile.ID,
		Statuses:  statuses,
		TagNames:  parseTagNames(c.Query("tags")),
		SortBy:    c.Query("sort", "latest_known_chapter"),
		Order:     c.Query("order", "desc"),
		Query:     c.Query("q"),
	}

	trackers, err := h.repo.List(c.Context(), options)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to list trackers", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to list trackers"})
	}

	return c.JSON(fiber.Map{"items": trackers})
}

func (h *TrackersHandler) GetByID(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return failProfileJSON(c, err)
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid tracker id"})
	}

	tracker, err := h.repo.GetByID(c.Context(), profile.ID, id)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to get tracker", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to get tracker"})
	}
	if tracker == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "tracker not found"})
	}

	return c.JSON(tracker)
}

func (h *TrackersHandler) Update(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return failProfileJSON(c, err)
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid tracker id"})
	}

	var req updateTrackerRequest
	if ok, err := parseTrackerBody(c, &req); !ok {
		return err
	}

	// Loaded before validating: a chapter number sent back unchanged is
	// checked against the stored one, not against the bounds (see
	// validateChapterEdit).
	existing, err := h.repo.GetByID(c.Context(), profile.ID, id)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to load tracker", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to update tracker"})
	}
	if existing == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "tracker not found"})
	}

	tracker, err := validateAndBuildTracker(req, existing)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	if ok, err := h.alignSourceWithURL(c, tracker); !ok {
		return err
	}

	// The request has no release-date field and the update writes every
	// column, so the stored date has to ride along, as it does on the edit
	// form. A PUT used to write NULL over it, even one echoing back the body GET
	// returned, which moved the tracker in the default latest-chapter sort and
	// took the release date off its card until the next poll.
	tracker.LatestReleaseAt = existing.LatestReleaseAt

	tracker.ProfileID = profile.ID

	updated, err := h.repo.Update(c.Context(), profile.ID, id, tracker)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to update tracker", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to update tracker"})
	}
	if updated == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "tracker not found"})
	}

	return c.JSON(updated)
}

func (h *TrackersHandler) Delete(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return failProfileJSON(c, err)
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid tracker id"})
	}

	deleted, err := h.repo.Delete(c.Context(), profile.ID, id)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "failed to delete tracker", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to delete tracker"})
	}
	if !deleted {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "tracker not found"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// validateAndBuildTracker checks a create or update body. stored is the
// tracker an update replaces, nil on create.
func validateAndBuildTracker(req createTrackerRequest, stored *models.Tracker) (*models.Tracker, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if req.SourceID <= 0 {
		return nil, fmt.Errorf("sourceId must be greater than zero")
	}
	sourceURL, err := validateSourceURL(req.SourceURL)
	if err != nil {
		return nil, fmt.Errorf("sourceUrl: %w", err)
	}
	status, err := validateTrackerStatus(req.Status)
	if err != nil {
		return nil, err
	}
	if err := validateTrackerRating(req.Rating); err != nil {
		return nil, err
	}
	var storedLastRead, storedLatest *float64
	if stored != nil {
		storedLastRead, storedLatest = stored.LastReadChapter, stored.LatestKnownChapter
	}
	if err := validateChapterEdit("lastReadChapter", req.LastReadChapter, storedLastRead); err != nil {
		return nil, err
	}
	if err := validateChapterEdit("latestKnownChapter", req.LatestKnownChapter, storedLatest); err != nil {
		return nil, err
	}

	var lastCheckedAt *time.Time
	if req.LastCheckedAt != nil && strings.TrimSpace(*req.LastCheckedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, *req.LastCheckedAt)
		if err != nil {
			return nil, fmt.Errorf("lastCheckedAt must be RFC3339")
		}
		// Normalized here because the store compares timestamps as text: a value
		// carrying its own offset would sort against the UTC ones by its local
		// clock face, hours off.
		utc := parsed.UTC()
		lastCheckedAt = &utc
	}

	return &models.Tracker{
		Title:              title,
		RelatedTitles:      searchutil.FilterEnglishAlphabetNames(req.RelatedTitles),
		SourceID:           req.SourceID,
		SourceItemID:       req.SourceItemID,
		SourceURL:          sourceURL,
		Status:             status,
		LastReadChapter:    req.LastReadChapter,
		Rating:             req.Rating,
		LatestKnownChapter: req.LatestKnownChapter,
		LastCheckedAt:      lastCheckedAt,
	}, nil
}

func parseStatuses(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	statuses := make([]string, 0, len(parts))
	for _, part := range parts {
		status := strings.TrimSpace(part)
		if status != "" {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

func validateStatuses(statuses []string) error {
	for _, status := range statuses {
		if !validStatuses[status] {
			return fmt.Errorf("invalid status filter")
		}
	}
	return nil
}
