package handlers

import (
	"context"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gofiber/fiber/v2"
)

type ConnectorsHandler struct {
	registry *connectors.Registry
}

func NewConnectorsHandler(registry *connectors.Registry) *ConnectorsHandler {
	return &ConnectorsHandler{registry: registry}
}

func (h *ConnectorsHandler) List(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"items": h.registry.List()})
}

func (h *ConnectorsHandler) Health(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 3*time.Second)
	defer cancel()
	return c.JSON(fiber.Map{"items": h.registry.Health(ctx)})
}
