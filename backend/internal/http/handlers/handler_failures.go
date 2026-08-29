package handlers

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// fail answers a failed request with publicMsg and puts the cause in the log.
// This deployment is a headless Pi whose only diagnostics are the container's
// slog output, and the HTTP layer used to fail in both of the ways that leave
// nothing usable: an error dropped here left the operator a sentence in a
// browser and an empty log, while an error sent as the response body published
// SQL text and host names to the page. One helper decides both directions.
func (h *DashboardHandler) fail(c *fiber.Ctx, status int, publicMsg string, err error) error {
	logHandlerFailure(c, status, publicMsg, err)
	return c.Status(status).SendString(publicMsg)
}

// logHandlerFailure records a failed request. The JSON endpoints call it
// directly, their response shape being their own. A 5xx is a defect nobody
// would otherwise hear about, so it is logged at error level; a 4xx is the
// normal answer to a bad request — a stale tracker id, an unknown profile —
// and error-level noise there would bury the 5xx this exists to surface.
func logHandlerFailure(c *fiber.Ctx, status int, publicMsg string, err error) {
	attrs := []any{"method", c.Method(), "path", c.Path(), "status", status, "message", publicMsg}
	if err != nil {
		attrs = append(attrs, "error", err)
	}

	if status >= fiber.StatusInternalServerError {
		slog.Error("http handler failed", attrs...)
		return
	}
	slog.Debug("http request rejected", attrs...)
}

// publicRequestMessage keeps a message that was written for the reader —
// fiber.NewError carries exactly that, and the handlers use it for the
// validation failures the user can act on — and replaces anything else with
// fallback. What else arrives here is a repository or connector error whose
// text names tables, files and hosts.
func publicRequestMessage(err error, fallback string) string {
	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) && strings.TrimSpace(fiberErr.Message) != "" {
		return fiberErr.Message
	}
	return fallback
}
