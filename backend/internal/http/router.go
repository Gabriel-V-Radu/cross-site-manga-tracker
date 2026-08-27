package http

import (
	"database/sql"

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
		connectorRegistry = connectordefaults.NewRegistry()
	}
	dashboard := handlers.NewDashboardHandler(db, connectorRegistry)
	connectorHandlers := handlers.NewConnectorsHandler(connectorRegistry)
	// The static handler sends only Last-Modified, which lets a browser invent
	// its own freshness lifetime from the file's age — long enough that a script
	// unchanged for months keeps being served from cache after a deploy, running
	// old code against a new server. Templates stamp asset URLs with the file's
	// mtime so the URL itself changes; this makes even an unversioned request
	// revalidate, so the two together leave no way to run a stale script. The
	// cost is a conditional request answered with 304.
	app.Use("/assets", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "no-cache")
		return c.Next()
	})
	app.Static("/assets", "./web/assets")
	app.Static("/uploads", "./data/uploads")
	// Locally stored cover art. File names hash the remote URL, so a cover
	// that changes upstream arrives as a new file — the old name never serves
	// different bytes, which is what makes immutable safe and cover renders
	// free after the first load.
	app.Use("/covers", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
		return c.Next()
	})
	app.Static("/covers", "./data/covers")
	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		return c.SendFile("./web/assets/favicon.svg")
	})
	app.Get("/", dashboard.Page)
	app.Get("/dashboard", dashboard.Page)
	app.Post("/dashboard/profile/rename", dashboard.RenameProfileFromForm)
	app.Get("/dashboard/profile/menu", dashboard.ProfileMenuModal)
	app.Get("/dashboard/profile/filter-tags", dashboard.ProfileFilterTagsPartial)
	app.Get("/dashboard/profile/filter-linked-sites", dashboard.ProfileFilterLinkedSitesPartial)
	app.Post("/dashboard/profile/switch", dashboard.SwitchProfileFromMenu)
	app.Post("/dashboard/profile/source-logos", dashboard.SaveSourceLogosFromMenu)
	app.Post("/dashboard/profile/tags", dashboard.CreateTagFromMenu)
	app.Post("/dashboard/profile/tags/rename", dashboard.RenameTagFromMenu)
	app.Post("/dashboard/profile/tags/delete", dashboard.DeleteTagFromMenu)
	app.Get("/dashboard/links", dashboard.LinksPage)
	app.Get("/dashboard/links/queue", dashboard.LinksQueuePartial)
	app.Post("/dashboard/links/scan", dashboard.StartLinkScan)
	app.Post("/dashboard/links/scan/stop", dashboard.StopLinkScan)
	app.Get("/dashboard/links/scan-status", dashboard.LinkScanStatus)
	app.Post("/dashboard/links/suggestions/:id/accept", dashboard.AcceptLinkSuggestion)
	app.Post("/dashboard/links/suggestions/:id/reject", dashboard.RejectLinkSuggestion)
	app.Post("/dashboard/links/trackers/:id/dismiss", dashboard.DismissLinkTracker)
	app.Post("/dashboard/links/trackers/:id/manual", dashboard.ManualLinkTracker)
	app.Post("/dashboard/links/accept-exact", dashboard.AcceptExactLinkMatches)
	app.Get("/dashboard/trackers", dashboard.TrackersPartial)
	app.Get("/dashboard/trackers/search", dashboard.SearchSourceTitles)
	app.Get("/dashboard/trackers/source-chapter", dashboard.ResolveSourceChapter)
	app.Get("/dashboard/trackers/empty-modal", dashboard.EmptyModal)
	app.Get("/dashboard/trackers/new", dashboard.NewTrackerModal)
	app.Get("/dashboard/trackers/:id/edit", dashboard.EditTrackerModal)
	app.Get("/dashboard/trackers/:id/card-fragment", dashboard.CardFragment)
	app.Post("/dashboard/trackers", dashboard.CreateFromForm)
	app.Post("/dashboard/trackers/:id", dashboard.UpdateFromForm)
	app.Post("/dashboard/trackers/:id/set-last-read", dashboard.SetLastReadFromCard)
	app.Post("/dashboard/trackers/:id/rating", dashboard.SetRatingFromCard)
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
