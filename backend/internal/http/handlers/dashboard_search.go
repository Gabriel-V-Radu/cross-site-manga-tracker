package handlers

import (
	"context"
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
	if source.Key == "mangafire" {
		searchTimeout = 12 * time.Second
	}

	ctx, cancel := context.WithTimeout(c.Context(), searchTimeout)
	defer cancel()

	if source.Key == "mangafire" {
		mangaURL, ok := extractMangaFireMangaURL(query)
		if !ok {
			return h.render(c, "tracker_search_results.html", trackerSearchResultsData{
				Query:      query,
				SourceID:   source.ID,
				SourceName: source.Name,
				Intent:     intent,
				Error:      "MangaFire search requires a full manga URL (https://mangafire.to/manga/{id})",
			})
		}

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
	if !strings.HasPrefix(strings.ToLower(parsed.Path), "/manga/") {
		return "", false
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}
