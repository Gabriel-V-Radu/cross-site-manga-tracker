package handlers

import (
	"database/sql"
	"time"

	"github.com/gofiber/fiber/v2"
)

type HealthHandler struct {
	db *sql.DB
}

func NewHealthHandler(db *sql.DB) *HealthHandler {
	return &HealthHandler{db: db}
}

func (h *HealthHandler) Check(c *fiber.Ctx) error {
	if err := h.db.Ping(); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "degraded",
			"db":     "down",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	}

	return c.JSON(fiber.Map{
		"status": "ok",
		"db":     "up",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}
