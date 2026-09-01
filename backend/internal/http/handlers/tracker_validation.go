package handlers

import (
	"fmt"
	"net/url"
	"strings"
)

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
