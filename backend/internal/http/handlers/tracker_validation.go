package handlers

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// maxChapterNumber bounds a chapter number typed into a form or sent to the
// API. The longest-running series reach a few thousand chapters (the highest
// in the deployed library is 2015.9), so this leaves room for any real number
// and for a site's odd numbering, while refusing what a typo or a script
// produces: -5 was stored and shown as "Ch. -5", and a 1e300 chapter rendered
// as 301 digits that made the page 2940px wide on a phone.
const maxChapterNumber = 100000

// validateChapterNumber accepts an absent chapter, or a finite number from 0 (a
// prologue) to maxChapterNumber. Decimals are real — extras are numbered like
// 1829.3 — so they pass. label names the field in the caller's own wording,
// since the API and the form spell their fields differently.
func validateChapterNumber(label string, value *float64) error {
	if value == nil {
		return nil
	}
	chapter := *value
	if math.IsNaN(chapter) || math.IsInf(chapter, 0) {
		return fmt.Errorf("%s must be a number", label)
	}
	if chapter < 0 || chapter > maxChapterNumber {
		return fmt.Errorf("%s must be between 0 and %d", label, maxChapterNumber)
	}
	return nil
}

// validateChapterEdit is validateChapterNumber for a save over a stored
// tracker, where a value equal to the stored one is not new input. The poller
// stores whatever number a site reports, and rows written before these bounds
// existed hold whatever was typed, so an edit that leaves a chapter alone — an
// API client sending back the body GET returned, a form saved for its title —
// must not be refused over a number the user did not touch.
func validateChapterEdit(label string, submitted *float64, stored *float64) error {
	if samePointedValue(submitted, stored) {
		return nil
	}
	return validateChapterNumber(label, submitted)
}

// sourceIDOwningURL returns the source a URL belongs to: the source of the
// connector that claims its host, or sourceID unchanged when none does. The
// URL is what gets polled, so the URL wins: a pair naming one site's id beside
// another site's link is one no connector can read, and the primary then
// failed on every poll ("url does not belong to mangahub" for a MangaFire
// link). The dashboard form and the JSON API both store through this.
func sourceIDOwningURL(ctx context.Context, registry *connectors.Registry, sources *repository.SourceRepository, sourceID int64, sourceURL string) (int64, error) {
	if registry == nil {
		return sourceID, nil
	}
	connector, ok := registry.GetByURL(sourceURL)
	if !ok {
		return sourceID, nil
	}
	owner, err := sources.GetByKey(ctx, connector.Key())
	if err != nil {
		return 0, err
	}
	if owner == nil {
		return sourceID, nil
	}
	return owner.ID, nil
}

// validStatuses is the closed set a tracker's status may take. The database
// enforces the same set with a CHECK constraint, so a value outside it used to
// surface as a 500 from the form path and a 400 from the JSON API; both paths
// now refuse it here, before anything is written.
var validStatuses = map[string]bool{
	"reading":      true,
	"completed":    true,
	"on_hold":      true,
	"dropped":      true,
	"plan_to_read": true,
}

// validateTrackerStatus normalizes a submitted status and rejects one outside
// the set. Empty is not an error here: the form path defaults it to "reading",
// the JSON API rejects it, and each caller says which before calling.
func validateTrackerStatus(raw string) (string, error) {
	status := strings.TrimSpace(raw)
	if !validStatuses[status] {
		return "", fmt.Errorf("invalid status")
	}
	return status, nil
}

// validateSourceURL accepts an absolute http(s) URL and nothing else. The value
// is stored, rendered as an href (html/template already neuters javascript:)
// and handed to the source's connector, which has no business receiving a
// mailto: or a bare word.
func validateSourceURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("source url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("source url must be an absolute http(s) address")
	}
	return trimmed, nil
}
