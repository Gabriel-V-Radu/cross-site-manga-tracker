package asuracomic

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

var (
	seriesHrefPattern          = regexp.MustCompile(`(?i)href=["'](?:https?://[^"']+)?/series/([a-z0-9-]+)["']`)
	htmlTagPattern             = regexp.MustCompile(`(?is)<[^>]+>`)
	whitespacePattern          = regexp.MustCompile(`\s+`)
	chapterHrefPattern         = regexp.MustCompile(`(?i)(?:/|[a-z0-9-]+/)?chapter/(\d+(?:\.\d+)?)`)
	metaTitlePattern           = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	titleTagPattern            = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	metaImagePattern           = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property=["']og:image["']|name=["']twitter:image["'])[^>]*content=["']([^"']+)["']`)
	updatedOnPattern           = regexp.MustCompile(`(?i)Updated\s+On\s*</[^>]+>\s*<[^>]+>\s*([A-Za-z]+\s+\d{1,2}(?:st|nd|rd|th)?\s+\d{4})`)
	monthDayOrdinalYearPattern = regexp.MustCompile(`(?i)(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+(\d{1,2})(?:st|nd|rd|th)?\s+(\d{4})`)
)

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     "https://asuracomic.net",
		allowedHost: []string{"asuracomic.net"},
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
	}
}

func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"asuracomic.net"}
	}
	return &Connector{baseURL: strings.TrimRight(baseURL, "/"), allowedHost: allowedHost, httpClient: client}
}

func (c *Connector) Key() string {
	return "asuracomic"
}

func (c *Connector) Name() string {
	return "AsuraComic"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.baseURL+"/series?page=1")
	return err
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return nil, fmt.Errorf("url does not belong to asuracomic")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "series" {
		return nil, fmt.Errorf("asuracomic url must match /series/{id}")
	}

	seriesID := strings.TrimSpace(segments[1])
	if seriesID == "" || !isValidSeriesID(seriesID) {
		return nil, fmt.Errorf("invalid asuracomic series id")
	}

	return c.resolveBySeriesID(ctx, seriesID)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(strings.ToLower(title))
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	body, err := c.fetchPage(ctx, c.baseURL+"/series?page=1&name="+url.QueryEscape(title))
	if err != nil {
		return nil, fmt.Errorf("fetch asuracomic search page: %w", err)
	}

	seriesIDs := collectUniqueSeriesIDs(body)
	if len(seriesIDs) == 0 {
		return []connectors.MangaResult{}, nil
	}

	results := make([]connectors.MangaResult, 0, limit)
	for _, seriesID := range seriesIDs {
		if len(results) >= limit {
			break
		}

		if !strings.Contains(strings.ToLower(seriesID), query) {
			anchorTitle := extractAnchorTextForSeriesID(body, seriesID)
			if anchorTitle != "" && !strings.Contains(strings.ToLower(anchorTitle), query) {
				continue
			}
		}

		resolved, resolveErr := c.resolveBySeriesID(ctx, seriesID)
		if resolveErr != nil {
			continue
		}

		if !matchesTitleQuery(resolved.Title, query) && !strings.Contains(strings.ToLower(resolved.SourceItemID), query) {
			continue
		}

		results = append(results, *resolved)
	}

	return results, nil
}

func (c *Connector) resolveBySeriesID(ctx context.Context, seriesID string) (*connectors.MangaResult, error) {
	body, err := c.fetchPage(ctx, c.baseURL+"/series/"+url.PathEscape(seriesID))
	if err != nil {
		return nil, fmt.Errorf("fetch series page: %w", err)
	}

	title := extractTitle(body)
	if title == "" {
		title = prettifySeriesID(seriesID)
	}

	latestChapter, releaseAtByChapter := extractLatestChapterAndReleaseAt(body, seriesID)
	coverImageURL := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaImagePattern, body)))
	coverImageURL = c.absoluteURL(coverImageURL)
	lastUpdatedAt := releaseAtByChapter
	if lastUpdatedAt == nil {
		lastUpdatedAt = extractLastUpdatedAt(body)
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  seriesID,
		Title:         title,
		URL:           "https://asuracomic.net/series/" + seriesID,
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: lastUpdatedAt,
	}, nil
}

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status: %d", res.StatusCode)
	}

	rawBody, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}

	return string(rawBody), nil
}

func (c *Connector) isAllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, allowed := range c.allowedHost {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

func (c *Connector) absoluteURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "//") {
		return "https:" + trimmed
	}
	if strings.HasPrefix(trimmed, "/") {
		return c.baseURL + trimmed
	}
	return c.baseURL + "/" + trimmed
}

func collectUniqueSeriesIDs(body string) []string {
	matches := seriesHrefPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	ids := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		seriesID := strings.TrimSpace(strings.ToLower(match[1]))
		if !isValidSeriesID(seriesID) {
			continue
		}
		if _, exists := seen[seriesID]; exists {
			continue
		}
		seen[seriesID] = struct{}{}
		ids = append(ids, seriesID)
	}

	return ids
}

func extractAnchorTextForSeriesID(body string, seriesID string) string {
	if seriesID == "" {
		return ""
	}
	pattern := regexp.MustCompile(`(?is)<a[^>]+href=["'](?:https?://[^"']+)?/series/` + regexp.QuoteMeta(seriesID) + `["'][^>]*>(.*?)</a>`)
	matches := pattern.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		candidate := cleanText(match[1])
		if candidate == "" || strings.EqualFold(candidate, "poster") || strings.EqualFold(candidate, "image") {
			continue
		}
		if chapterIndex := strings.Index(strings.ToLower(candidate), " chapter "); chapterIndex > 0 {
			candidate = strings.TrimSpace(candidate[:chapterIndex])
		}
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func extractTitle(body string) string {
	title := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaTitlePattern, body)))
	if title == "" {
		title = strings.TrimSpace(html.UnescapeString(cleanText(firstSubmatch(titleTagPattern, body))))
	}
	if title == "" {
		return ""
	}

	title = strings.ReplaceAll(title, "- Asura Scans", "")
	title = strings.ReplaceAll(title, "| Asura Scans", "")
	title = strings.TrimSpace(title)
	return title
}

func extractLatestChapterAndReleaseAt(body string, seriesID string) (*float64, *time.Time) {
	chapterPattern := chapterHrefPattern
	if strings.TrimSpace(seriesID) != "" {
		chapterPattern = regexp.MustCompile(`(?i)(?:/series/)?` + regexp.QuoteMeta(seriesID) + `/chapter/(\d+(?:\.\d+)?)`)
	}

	chapterIndexes := chapterPattern.FindAllStringSubmatchIndex(body, -1)
	if len(chapterIndexes) == 0 && strings.TrimSpace(seriesID) != "" {
		chapterIndexes = chapterHrefPattern.FindAllStringSubmatchIndex(body, -1)
	}
	if len(chapterIndexes) == 0 {
		return nil, nil
	}

	var latestByPair *float64
	var releaseAtByPair *time.Time
	for _, loc := range chapterIndexes {
		if len(loc) < 4 {
			continue
		}

		chapterRaw := body[loc[2]:loc[3]]
		parsedChapter, chapterErr := strconv.ParseFloat(strings.TrimSpace(chapterRaw), 64)
		if chapterErr != nil {
			continue
		}

		segmentStart := loc[0]
		segmentEnd := segmentStart + 2200
		if segmentEnd > len(body) {
			segmentEnd = len(body)
		}
		if segmentStart < 0 || segmentStart >= len(body) || segmentStart >= segmentEnd {
			continue
		}

		segment := body[segmentStart:segmentEnd]
		dateRaw := monthDayOrdinalYearPattern.FindString(segment)
		parsedDate := parseAsuraDate(dateRaw)

		if latestByPair == nil || parsedChapter > *latestByPair {
			chapterValue := parsedChapter
			latestByPair = &chapterValue
			if parsedDate != nil {
				dateValue := *parsedDate
				releaseAtByPair = &dateValue
			} else {
				releaseAtByPair = nil
			}
		}
	}

	if latestByPair != nil {
		return latestByPair, releaseAtByPair
	}

	return nil, nil
}

func extractLastUpdatedAt(body string) *time.Time {
	raw := strings.TrimSpace(firstSubmatch(updatedOnPattern, body))
	if raw != "" {
		if parsed := parseAsuraDate(raw); parsed != nil {
			return parsed
		}
	}

	allDates := monthDayOrdinalYearPattern.FindAllString(body, -1)
	var latest *time.Time
	for _, rawDate := range allDates {
		parsed := parseAsuraDate(rawDate)
		if parsed == nil {
			continue
		}
		if latest == nil || parsed.After(*latest) {
			copyValue := *parsed
			latest = &copyValue
		}
	}

	return latest
}

func parseAsuraDate(raw string) *time.Time {
	matches := monthDayOrdinalYearPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(matches) < 4 {
		return nil
	}

	normalized := fmt.Sprintf("%s %s %s", strings.Title(strings.ToLower(matches[1])), matches[2], matches[3])
	parsed, err := time.Parse("January 2 2006", normalized)
	if err != nil {
		parsed, err = time.Parse("Jan 2 2006", normalized)
		if err != nil {
			return nil
		}
	}
	utc := parsed.UTC()
	return &utc
}

func isValidSeriesID(seriesID string) bool {
	for _, r := range seriesID {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return seriesID != "" && !strings.Contains(seriesID, "/chapter/")
}

func prettifySeriesID(seriesID string) string {
	trimmed := strings.TrimSpace(seriesID)
	if trimmed == "" {
		return "Untitled"
	}

	trimmed = strings.ReplaceAll(trimmed, "-", " ")
	parts := strings.Fields(trimmed)
	for index, part := range parts {
		parts[index] = strings.Title(part)
	}
	return strings.Join(parts, " ")
}

func cleanText(raw string) string {
	text := htmlTagPattern.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	text = whitespacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func firstSubmatch(pattern *regexp.Regexp, raw string) string {
	matches := pattern.FindStringSubmatch(raw)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func matchesTitleQuery(title string, query string) bool {
	normalizedTitle := strings.TrimSpace(strings.ToLower(title))
	normalizedQuery := strings.TrimSpace(strings.ToLower(query))
	if normalizedTitle == "" || normalizedQuery == "" {
		return false
	}
	return strings.Contains(normalizedTitle, normalizedQuery)
}
