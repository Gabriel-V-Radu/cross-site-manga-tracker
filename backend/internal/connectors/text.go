package connectors

import (
	"html"
	"regexp"
	"strings"
)

var (
	htmlTagPattern        = regexp.MustCompile(`(?is)<[^>]+>`)
	htmlWhitespacePattern = regexp.MustCompile(`\s+`)
)

// FirstSubmatch returns pattern's first capture group in raw, or "" when the
// pattern does not match. The HTML-scraping connectors live on this shape.
func FirstSubmatch(pattern *regexp.Regexp, raw string) string {
	matches := pattern.FindStringSubmatch(raw)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// CleanText reduces an HTML fragment to its readable text: tags stripped,
// entities unescaped, whitespace collapsed and trimmed.
func CleanText(raw string) string {
	text := htmlTagPattern.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	text = htmlWhitespacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// PrettifySlug turns a URL slug into a display title: dashes become spaces and
// each word is capitalized ("nano-machine" → "Nano Machine"). An empty slug
// stays empty — callers that need a placeholder choose their own.
func PrettifySlug(slug string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(slug, "-", " "))
	if trimmed == "" {
		return ""
	}
	parts := strings.Fields(trimmed)
	for index := range parts {
		parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
	}
	return strings.Join(parts, " ")
}
