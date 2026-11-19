package http

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/http/handlers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
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

	// Default recover.New swallows panics without logging, and its own stack
	// trace goes to stderr only. The server's log is the one in the data
	// volume (LOG_FILE), which outlives the container; a panic that is not in
	// it is one nobody can look into after the next deploy.
	app.Use(recover.New(recover.Config{EnableStackTrace: true, StackTraceHandler: logPanic}))

	// Every page and partial is text that compresses to a fifth or less — a
	// page of cards is some hundred kilobytes of HTML, the scripts and the
	// stylesheet two hundred more — and all of it went out as it was. On the
	// LAN that is nothing; on a phone reaching the Pi from outside it is most
	// of the wait. Best speed, since the Pi does the compressing: it takes
	// nearly all of the saving for a fraction of the default level's time.
	//
	// No route is exempted, and none needs to be: what gets compressed is
	// decided by the answer's content type (fasthttp: text, application/*,
	// SVG, fonts), so a JPEG, PNG or WebP cover goes out as it is from
	// whichever route serves it, while an SVG logo under /uploads, which is
	// text, is compressed like any other.
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))

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
	// mtime (?v=), so the URL itself changes when the file does.
	//
	// A stamped URL therefore never serves different bytes and is cached for
	// good: every page load used to ask about each script and stylesheet again,
	// a conditional request apiece answered 304 before the page could run. An
	// unstamped request still revalidates, so the two together leave no way to
	// run a stale script. The no-cache is set ahead of the static handler so it
	// also rides on a 404; the long lifetime only on a file actually served.
	app.Use("/assets", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "no-cache")
		return c.Next()
	})
	app.Static("/assets", cfg.AssetsDir(), fiber.Static{
		ModifyResponse: func(c *fiber.Ctx) error {
			if c.Query("v") != "" && c.Response().StatusCode() < fiber.StatusBadRequest {
				c.Set(fiber.HeaderCacheControl, immutableCacheControl)
			}
			return nil
		},
	})
	// Uploaded site logos. Each upload is stored under a new name (the time
	// of the upload is in it), so a name never serves different bytes either.
	app.Static("/uploads", cfg.UploadsDir(), fiber.Static{
		ModifyResponse: func(c *fiber.Ctx) error {
			if c.Response().StatusCode() < fiber.StatusBadRequest {
				c.Set(fiber.HeaderCacheControl, immutableCacheControl)
			}
			return nil
		},
	})
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
				c.Set(fiber.HeaderCacheControl, immutableCacheControl)
			}
			return nil
		},
	})
	// What a browser asks for on its own, whatever the page's <link> says.
	// The name is fixed, so it is cached for a day rather than for good.
	faviconPath := filepath.Join(cfg.AssetsDir(), "favicon.svg")
	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		if err := c.SendFile(faviconPath); err != nil {
			return err
		}
		c.Set(fiber.HeaderCacheControl, "public, max-age=86400")
		return nil
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

// immutableCacheControl is for a URL whose bytes never change: a year, and no
// revalidation in between.
const immutableCacheControl = "public, max-age=31536000, immutable"

// logPanic records a recovered panic, with its stack, in the server's log.
func logPanic(c *fiber.Ctx, recovered any) {
	slog.Error("http handler panicked",
		"method", c.Method(), "path", c.Path(),
		"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
}

// errorHandler answers the failures no handler wrote a response for — an
// unknown route, a method the route does not take, a recovered panic. Under
// /v1 they keep the API's JSON shape: an unknown endpoint used to answer
// "Cannot GET /v1/…" as plain text, which a client decoding every answer as
// {"message": …} could not read. Everywhere else the answer is plain text,
// which is what the dashboard shows next to a form.
//
// A fiber.Error carries a message written for whoever made the request and is
// answered with it. Anything else is a defect — a panic, an error a handler
// let through — and its text can name files, tables and hosts: outside /v1 it
// used to be sent to the browser as it was (fiber's default handler) and
// logged nowhere. It is logged here, as is every 5xx, and the browser is told
// only that the server failed.
func errorHandler(c *fiber.Ctx, err error) error {
	status, message := fiber.StatusInternalServerError, "Internal Server Error"
	var fiberErr *fiber.Error
	isRequestError := errors.As(err, &fiberErr)
	if isRequestError {
		status, message = fiberErr.Code, fiberErr.Message
	}
	if status >= fiber.StatusInternalServerError {
		slog.Error("http handler failed", "method", c.Method(), "path", c.Path(), "status", status, "error", err)
	}

	if isAPIPath(c.Path()) {
		if !isRequestError {
			message = "internal server error"
		}
		return c.Status(status).JSON(fiber.Map{"message": message})
	}
	c.Set(fiber.HeaderContentType, fiber.MIMETextPlainCharsetUTF8)
	return c.Status(status).SendString(message)
}

func isAPIPath(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}
