package http

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/http/handlers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// BuildServer reports the startup failures a long-lived process must not
// survive — today a template set that does not parse, which would otherwise
// deploy "successfully" and answer every dashboard page with a 500 until
// somebody restarts it. The app is returned either way, so a caller that
// chooses to carry on still has one. A nil registry gets the default
// connectors; the tests pass an explicit one so nothing reaches the network.
func BuildServer(cfg config.Config, db *sql.DB, connectorRegistry *connectors.Registry) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		AppName: cfg.AppName,
		// Without these fasthttp keeps stalled connections forever; on the Pi
		// dead clients on flaky Wi-Fi accumulate. WriteTimeout stays generous
		// because live source searches can legitimately take tens of seconds.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  2 * time.Minute,
		ErrorHandler: errorHandler,
	})

	// Default recover.New swallows panics without logging; the stack trace is
	// the only way to debug a panicking handler from docker logs.
	app.Use(recover.New(recover.Config{EnableStackTrace: true}))

	// Every dashboard mutation is a plain form POST with no token, and the
	// profile resolver falls back to the default profile when no cookie comes
	// along — so a form auto-submitted from any other site the reader has open
	// could delete or rewrite trackers on the LAN instance. Browsers tell us
	// where a request came from; refusing the ones from another site is the
	// whole defence this app needs, since it has no login to protect. The JSON
	// API has it too. It used to decode a form-encoded body, so an
	// auto-submitted form could POST junk trackers to /v1/trackers; its writes
	// now take only JSON, which a browser will not send to another site
	// without a preflight this server never grants, and the guard stays so the
	// two do not depend on that.
	app.Use("/dashboard", rejectCrossSiteWrites)
	app.Use("/v1", rejectCrossSiteWrites)

	health := handlers.NewHealthHandler(db)
	if connectorRegistry == nil {
		connectorRegistry = connectordefaults.NewRegistry()
	}
	trackers := handlers.NewTrackersHandler(db, connectorRegistry)
	dashboard := handlers.NewDashboardHandler(db, connectorRegistry, handlers.DashboardPaths{
		TemplatesGlob:  cfg.TemplatesGlob(),
		AssetsDir:      cfg.AssetsDir(),
		CoversDir:      cfg.CoversDir(),
		SourceLogosDir: cfg.SourceLogosDir(),
	})
	// The dashboard sweeps its caches on a background ticker and can be
	// running a link scan; tying both to the app's life keeps a shut-down app
	// — every one a test binary builds — from leaving goroutines behind, and
	// keeps a scan from issuing requests and writes while the process is
	// closing the database under it.
	app.Hooks().OnShutdown(func() error {
		dashboard.Close()
		return nil
	})
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
	app.Static("/assets", cfg.AssetsDir())
	app.Static("/uploads", cfg.UploadsDir())
	// Locally stored cover art. File names hash the remote URL, so a cover
	// that changes upstream arrives as a new file — the old name never serves
	// different bytes, which is what makes immutable safe and cover renders
	// free after the first load. Only a file actually served gets it: set ahead
	// of the static handler, the header rode along on the 404 for a missing
	// file too, and a browser could keep that miss cached for a year after the
	// cover had been fetched.
	app.Static("/covers", cfg.CoversDir(), fiber.Static{
		ModifyResponse: func(c *fiber.Ctx) error {
			if c.Response().StatusCode() < fiber.StatusBadRequest {
				c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
			}
			return nil
		},
	})
	faviconPath := filepath.Join(cfg.AssetsDir(), "favicon.svg")
	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		return c.SendFile(faviconPath)
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
	app.Get("/dashboard/sources", dashboard.SourceHealthModal)
	app.Get("/dashboard/sources/indicator", dashboard.SourceHealthIndicator)
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
	app.Get("/dashboard/remote-cover", dashboard.RemoteCover)
	app.Get("/dashboard/trackers/source-chapter", dashboard.ResolveSourceChapter)
	app.Get("/dashboard/trackers/empty-modal", dashboard.EmptyModal)
	app.Get("/dashboard/trackers/new", dashboard.NewTrackerModal)
	app.Get("/dashboard/trackers/:id/edit", dashboard.EditTrackerModal)
	app.Get("/dashboard/trackers/:id/card-fragment", dashboard.CardFragment)
	app.Get("/dashboard/trackers/:id/info", dashboard.TrackerInfo)
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

	if err := dashboard.TemplateError(); err != nil {
		return app, fmt.Errorf("parse dashboard templates: %w", err)
	}

	return app, nil
}

// rejectCrossSiteWrites refuses a state-changing request that a browser says
// originated on another site. Two signals, either one enough: the Sec-Fetch-Site
// header every current browser sends, and the Origin header sent with every
// cross-origin POST, compared against the host the request arrived on. A client
// that sends neither (curl, the test harness, an old browser) is let through —
// this guards against the reader's own browser being used against them, not
// against a script that can already reach the port.
func rejectCrossSiteWrites(c *fiber.Ctx) error {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return c.Next()
	}

	// Returned rather than written, so errorHandler gives it each route's own
	// shape: plain text for the dashboard, {"message": …} under /v1.
	refused := fiber.NewError(fiber.StatusForbidden, "Cross-site request refused")

	site := strings.ToLower(strings.TrimSpace(c.Get("Sec-Fetch-Site")))
	if site == "cross-site" {
		return refused
	}

	origin := strings.TrimSpace(c.Get(fiber.HeaderOrigin))
	if strings.EqualFold(origin, "null") {
		// An opaque origin: a sandboxed frame, a file: page or a data: URL —
		// the very places a hostile form can be served from, and it used to
		// pass as if no Origin had been sent. A browser also sends it for its
		// own page's POST under a no-referrer policy, and one that says
		// same-origin in Sec-Fetch-Site, which a page cannot forge, is that.
		if site != "same-origin" {
			return refused
		}
		return c.Next()
	}
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, c.Hostname()) {
			return refused
		}
	}

	return c.Next()
}

// errorHandler answers the failures no handler wrote a response for — an
// unknown route, a method the route does not take, a recovered panic. Under
// /v1 they keep the API's JSON shape: an unknown endpoint used to answer
// "Cannot GET /v1/…" as plain text, which a client decoding every answer as
// {"message": …} could not read. Everywhere else fiber's own plain-text answer
// stays, which is what the dashboard shows next to a form.
func errorHandler(c *fiber.Ctx, err error) error {
	if !isAPIPath(c.Path()) {
		return fiber.DefaultErrorHandler(c, err)
	}

	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		return c.Status(fiberErr.Code).JSON(fiber.Map{"message": fiberErr.Message})
	}
	// Anything else is a defect, and its text can name files and tables.
	slog.Error("http handler failed", "method", c.Method(), "path", c.Path(), "error", err)
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"message": "internal server error"})
}

func isAPIPath(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}
