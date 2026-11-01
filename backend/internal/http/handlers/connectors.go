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

// connectorsHealthBudget is the same 20s the dashboard's own sweep gives
// registry.Health. Each check is a real request through the shared per-host
// throttle and waits for its slot inside this context; with 3s, a probe queued
// behind the poller on a host spaced 3.5s apart (api.comick.dev) reported a
// healthy site as failing with "context deadline exceeded".
const connectorsHealthBudget = 20 * time.Second

func (h *ConnectorsHandler) Health(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), connectorsHealthBudget)
	defer cancel()
	return c.JSON(fiber.Map{"items": h.registry.Health(ctx)})
}
