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

func (h *DashboardHandler) SearchSourceTitles(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))
	intent := strings.TrimSpace(c.Query("intent"))
	if intent == "" {
		intent = "primary"
	}
	if query == "" {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Intent: intent})
	}

	sourceID, err := strconv.ParseInt(strings.TrimSpace(c.Query("source_id")), 10, 64)
	if err != nil || sourceID <= 0 {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Select a source first", Intent: intent})
	}

	source, err := h.sourceRepo.GetByID(sourceID)
	if err != nil {
		// Answered as a rendered results box rather than a status, so the log
		// is the only place this failure is visible to the operator.
		slog.Error("source lookup failed", "path", c.Path(), "source", sourceID, "error", err)
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Failed to resolve source", Intent: intent})
	}
	if source == nil || !source.Enabled {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Source not found or disabled", Intent: intent})
	}

	connector, ok := h.registry.Get(source.Key)
	if !ok {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "No connector registered for selected source", Intent: intent})
	}

	searchTimeout := 5 * time.Second
	if source.Key == "mangafire" || source.Key == "freewebnovel" {
		// Both sit behind Cloudflare and need extra time: mangafire paces its
		// API, freewebnovel warms a homepage hit for a clearance cookie before
		// searching (and may retry once), which adds a round-trip.
		searchTimeout = 12 * time.Second
	}

	ctx, cancel := context.WithTimeout(c.Context(), searchTimeout)
	defer cancel()

	if mangaURL, ok := extractMangaFireMangaURL(query); source.Key == "mangafire" && ok {
		resolved, resolveErr := connector.ResolveByURL(ctx, mangaURL)
		if resolveErr != nil || resolved == nil {
			message := "Failed to resolve MangaFire URL"
			if resolveErr != nil {
				message = "Failed to resolve MangaFire URL: " + resolveErr.Error()
			}
			return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
				Query:      query,
				SourceID:   source.ID,
				SourceName: source.Name,
				Intent:     intent,
				Error:      message,
			})
		}

		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
			Items:      []connectors.MangaResult{*resolved},
			Query:      query,
			SourceID:   source.ID,
			SourceName: source.Name,
			Intent:     intent,
		})
	}

	results, err := connector.SearchByTitle(ctx, query, 8)
	if err != nil {
		return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Query: query, Error: "Search failed for this source: " + err.Error(), SourceID: source.ID, SourceName: source.Name, Intent: intent})
	}

	return h.render(c, "tracker_search_results.html", trackerSearchResultsData{Items: results, Query: query, SourceID: source.ID, SourceName: source.Name, Intent: intent})
}

// sourceChapterResponse carries the two fields the tracker form cannot fill
// from a search result, already formatted the way the inputs expect them.
type sourceChapterResponse struct {
	LatestChapter   string `json:"latestChapter"`
	LatestReleaseAt string `json:"latestReleaseAt"`
	Error           string `json:"error,omitempty"`
}

// ResolveSourceChapter reports the latest chapter a source has for one title.
// Some search listings cannot carry a chapter number — MangaFire's spans every
// language it hosts and MangaBuddy's has no chapters in it at all — so the form
// asks for it once the user has picked a result, which is also the only title
// out of the results worth spending a request on.
func (h *DashboardHandler) ResolveSourceChapter(c *fiber.Ctx) error {
	sourceURL := strings.TrimSpace(c.Query("url"))
	if sourceURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sourceChapterResponse{Error: "url is required"})
	}

	sourceID, err := strconv.ParseInt(strings.TrimSpace(c.Query("source_id")), 10, 64)
	if err != nil || sourceID <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(sourceChapterResponse{Error: "source_id is required"})
	}

	source, err := h.sourceRepo.GetByID(sourceID)
	if err != nil {
		logHandlerFailure(c, fiber.StatusInternalServerError, "Failed to resolve source", err)
		return c.Status(fiber.StatusInternalServerError).JSON(sourceChapterResponse{Error: "Failed to resolve source"})
	}
	if source == nil || !source.Enabled {
		return c.Status(fiber.StatusNotFound).JSON(sourceChapterResponse{Error: "Source not found or disabled"})
	}

	connector, ok := h.registry.Get(source.Key)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(sourceChapterResponse{Error: "No connector registered for selected source"})
	}

	// Same allowance as a search on this source: resolving reads the title and
	// then its chapter listing, and the paced sources need room for both.
	timeout := 5 * time.Second
	if source.Key == "mangafire" || source.Key == "freewebnovel" {
		timeout = 12 * time.Second
	}

	ctx, cancel := context.WithTimeout(c.Context(), timeout)
	defer cancel()

	resolved, err := connector.ResolveByURL(ctx, sourceURL)
	if err != nil || resolved == nil {
		// The form stays usable with the field left blank — the user can type a
		// number, and the first poll fills it in either way — so a failure here
		// is reported rather than made to look like "no chapters".
		return c.Status(fiber.StatusOK).JSON(sourceChapterResponse{Error: "Could not read the latest chapter from " + source.Name})
	}

	return c.JSON(sourceChapterResponse{
		LatestChapter:   chapterInputValue(resolved.LatestChapter),
		LatestReleaseAt: timeInputValue(resolved.LastUpdatedAt),
	})
}

func extractMangaFireMangaURL(query string) (string, bool) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", false
	}

	if !strings.HasPrefix(strings.ToLower(trimmed), "http://") && !strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		return "", false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Hostname(), "mangafire.to") && !strings.EqualFold(parsed.Hostname(), "www.mangafire.to") {
		return "", false
	}
	lowerPath := strings.ToLower(parsed.Path)
	if !strings.HasPrefix(lowerPath, "/manga/") && !strings.HasPrefix(lowerPath, "/title/") {
		return "", false
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}
