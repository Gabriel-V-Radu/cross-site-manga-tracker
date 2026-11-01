package handlers

import (
	"context"
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

// healthPingTimeout bounds the database check. The pool holds one connection,
// so a plain Ping made while that connection is held waits for it with no
// deadline: a wedged connection used to leave every probe hanging, one more
// blocked goroutine per minute, instead of answering 503.
const healthPingTimeout = 2 * time.Second

func (h *HealthHandler) Check(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), healthPingTimeout)
	defer cancel()
	if err := h.db.PingContext(ctx); err != nil {
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
