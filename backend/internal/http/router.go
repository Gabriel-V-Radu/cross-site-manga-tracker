package http

import (
	"database/sql"
	"log/slog"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/http/handlers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func NewServer(cfg config.Config, db *sql.DB) *fiber.App {
	return NewServerWithRegistry(cfg, db, nil)
}

func NewServerWithRegistry(cfg config.Config, db *sql.DB, connectorRegistry *connectors.Registry) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: cfg.AppName,
	})

	app.Use(recover.New())

	health := handlers.NewHealthHandler(db)
	trackers := handlers.NewTrackersHandler(db)
	if connectorRegistry == nil {
		loadedRegistry, err := connectordefaults.NewRegistry(cfg.YAMLConnectorsPath)
		if err != nil {
			slog.Warn("yaml connectors loaded with warnings", "error", err)
		}
		connectorRegistry = loadedRegistry
	}
	dashboard := handlers.NewDashboardHandler(db, connectorRegistry)
	connectorHandlers := handlers.NewConnectorsHandler(connectorRegistry)
	app.Static("/assets", "./web/assets")
	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		return c.SendFile("./web/assets/favicon.svg")
	})
	app.Get("/", dashboard.Page)
	app.Get("/dashboard", dashboard.Page)
	app.Get("/dashboard/trackers", dashboard.TrackersPartial)
	app.Get("/dashboard/trackers/search", dashboard.SearchSourceTitles)
	app.Get("/dashboard/trackers/empty-modal", dashboard.EmptyModal)
	app.Get("/dashboard/trackers/new", dashboard.NewTrackerModal)
	app.Get("/dashboard/trackers/:id/edit", dashboard.EditTrackerModal)
	app.Post("/dashboard/trackers", dashboard.CreateFromForm)
	app.Post("/dashboard/trackers/:id", dashboard.UpdateFromForm)
	app.Post("/dashboard/trackers/:id/set-last-read", dashboard.SetLastReadFromCard)
	app.Post("/dashboard/trackers/:id/delete", dashboard.DeleteFromForm)
	app.Get("/health", health.Check)
	app.Get("/v1/health", health.Check)

	v1 := app.Group("/v1")
	v1.Get("/connectors", connectorHandlers.List)
	v1.Get("/connectors/health", connectorHandlers.Health)
	v1.Post("/trackers", trackers.Create)
	v1.Get("/trackers", trackers.List)
	v1.Get("/trackers/:id", trackers.GetByID)
	v1.Put("/trackers/:id", trackers.Update)
	v1.Delete("/trackers/:id", trackers.Delete)

	return app
}
