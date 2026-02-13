package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

type DashboardHandler struct {
	trackerRepo *repository.TrackerRepository
	sourceRepo  *repository.SourceRepository
}

type dashboardPageData struct {
	Statuses []string
	Sorts    []string
}

type trackersPartialData struct {
	Trackers []models.Tracker
	ViewMode string
}

type trackerFormData struct {
	Mode    string
	Tracker *models.Tracker
	Sources []models.Source
}

func NewDashboardHandler(db *sql.DB) *DashboardHandler {
	return &DashboardHandler{
		trackerRepo: repository.NewTrackerRepository(db),
		sourceRepo:  repository.NewSourceRepository(db),
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

	return h.render(c, "trackers_partial.html", trackersPartialData{Trackers: items, ViewMode: viewMode})
}

func (h *DashboardHandler) NewTrackerModal(c *fiber.Ctx) error {
	sources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load sources")
	}
	return h.render(c, "tracker_form_modal.html", trackerFormData{Mode: "create", Sources: sources})
}

func (h *DashboardHandler) EmptyModal(c *fiber.Ctx) error {
	return h.render(c, "empty_modal.html", nil)
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

	return h.render(c, "tracker_form_modal.html", trackerFormData{Mode: "edit", Tracker: tracker, Sources: sources})
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

func (h *DashboardHandler) render(c *fiber.Ctx, templateName string, data any) error {
	tmpl, err := template.ParseGlob("web/templates/*.html")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Template load error")
	}
	c.Type("html", "utf-8")
	return tmpl.ExecuteTemplate(c.Response().BodyWriter(), templateName, data)
}
