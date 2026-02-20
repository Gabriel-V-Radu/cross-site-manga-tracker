package handlers

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

var validStatuses = map[string]bool{
	"reading":      true,
	"completed":    true,
	"on_hold":      true,
	"dropped":      true,
	"plan_to_read": true,
}

type createTrackerRequest struct {
	Title              string   `json:"title"`
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
	profileResolver *profileContextResolver
}

func NewTrackersHandler(db *sql.DB) *TrackersHandler {
	return &TrackersHandler{
		repo:            repository.NewTrackerRepository(db),
		profileResolver: newProfileContextResolver(db),
	}
}

func (h *TrackersHandler) Create(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	var req createTrackerRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid json body"})
	}

	tracker, err := validateAndBuildTracker(req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	exists, err := h.repo.SourceExists(tracker.SourceID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to validate source"})
	}
	if !exists {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "sourceId does not exist"})
	}

	tracker.ProfileID = profile.ID

	created, err := h.repo.Create(tracker)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to create tracker"})
	}

	return c.Status(fiber.StatusCreated).JSON(created)
}

func (h *TrackersHandler) List(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
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

	trackers, err := h.repo.List(options)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to list trackers"})
	}

	return c.JSON(fiber.Map{"items": trackers})
}

func (h *TrackersHandler) GetByID(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid tracker id"})
	}

	tracker, err := h.repo.GetByID(profile.ID, id)
	if err != nil {
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid tracker id"})
	}

	var req updateTrackerRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid json body"})
	}

	tracker, err := validateAndBuildTracker(req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	exists, err := h.repo.SourceExists(tracker.SourceID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to validate source"})
	}
	if !exists {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "sourceId does not exist"})
	}

	tracker.ProfileID = profile.ID

	updated, err := h.repo.Update(profile.ID, id, tracker)
	if err != nil {
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": err.Error()})
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "invalid tracker id"})
	}

	deleted, err := h.repo.Delete(profile.ID, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "failed to delete tracker"})
	}
	if !deleted {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "tracker not found"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func validateAndBuildTracker(req createTrackerRequest) (*models.Tracker, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if req.SourceID <= 0 {
		return nil, fmt.Errorf("sourceId must be greater than zero")
	}
	if strings.TrimSpace(req.SourceURL) == "" {
		return nil, fmt.Errorf("sourceUrl is required")
	}
	status := strings.TrimSpace(req.Status)
	if !validStatuses[status] {
		return nil, fmt.Errorf("invalid status")
	}
	if err := validateTrackerRating(req.Rating); err != nil {
		return nil, err
	}

	var lastCheckedAt *time.Time
	if req.LastCheckedAt != nil && strings.TrimSpace(*req.LastCheckedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, *req.LastCheckedAt)
		if err != nil {
			return nil, fmt.Errorf("lastCheckedAt must be RFC3339")
		}
		lastCheckedAt = &parsed
	}

	return &models.Tracker{
		Title:              title,
		SourceID:           req.SourceID,
		SourceItemID:       req.SourceItemID,
		SourceURL:          strings.TrimSpace(req.SourceURL),
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
