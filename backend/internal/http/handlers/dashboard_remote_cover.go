package handlers

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gofiber/fiber/v2"
)

// remoteCoverPath is where the router mounts RemoteCover. The two have to
// agree: coverSrc hands this path straight to the browser as an image src.
const remoteCoverPath = "/dashboard/remote-cover"

// remoteCoverMaxAge lets the browser keep a relayed cover. The URLs relayed
// today (MangaDex's) name a file per upload, so the bytes behind one never
// change; a day still bounds a stale image if that ever stops being true.
const remoteCoverMaxAge = 24 * time.Hour

// coverSrc is the src a remote cover gets on the page. A host that answers the
// reader's browser with a bot challenge (MangaDex since 2026-11) would leave the
// <img> broken, so its covers go through RemoteCover; every other URL — a
// hotlinkable CDN, a local /covers path — is used as it is.
func coverSrc(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || !connectors.ChallengesBrowsers(parsed.Hostname()) {
		return trimmed
	}
	return remoteCoverPath + "?url=" + url.QueryEscape(trimmed)
}

// RemoteCover relays one cover image from a host the browser cannot load it
// from itself. It only relays https URLs on those hosts, so it cannot be used as
// a general-purpose fetcher, and the image goes through the cover resolver's
// guarded client and image check like any stored cover.
func (h *DashboardHandler) RemoteCover(c *fiber.Ctx) error {
	target, err := url.Parse(strings.TrimSpace(c.Query("url")))
	if err != nil || !strings.EqualFold(target.Scheme, "https") || !connectors.ChallengesBrowsers(target.Hostname()) {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
	defer cancel()

	body, ext, ok := h.covers.FetchImage(ctx, target.String())
	if !ok {
		slog.Debug("remote cover relay failed", "url", target.String())
		return c.SendStatus(fiber.StatusBadGateway)
	}

	c.Set(fiber.HeaderCacheControl, "public, max-age="+strconv.Itoa(int(remoteCoverMaxAge/time.Second)))
	c.Type(ext)
	return c.Send(body)
}
