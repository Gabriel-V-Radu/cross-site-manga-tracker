package flamecomics

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
	seriesIDPattern               = regexp.MustCompile(`^\d+$`)
	seriesAnchorPattern           = regexp.MustCompile(`(?is)<a[^>]+href=["'](?:https?://[^"']+)?/series/(\d+)["'][^>]*>(.*?)</a>`)
	metaTitlePattern              = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^\"]+)["']`)
	titleTagPattern               = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	metaImagePattern              = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property=["']og:image["']|name=["']twitter:image["'])[^>]*content=["']([^\"]+)["']`)
	chapterBySeriesPattern        = regexp.MustCompile(`(?i)/series/(\d+)/[a-z0-9]+`)
	chapterNumberPattern          = regexp.MustCompile(`(?i)Chapter(?:\s|<!--\s*-->|&nbsp;)+([0-9]+(?:\.[0-9]+)?)`)
	fullDateTimePattern           = regexp.MustCompile(`(?i)(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+\d{1,2},\s+\d{4}(?:\s+\d{1,2}:\d{2}\s*(?:AM|PM))?`)
	htmlTagPattern                = regexp.MustCompile(`(?is)<[^>]+>`)
	whitespacePattern             = regexp.MustCompile(`\s+`)
	trailingRegionCodeTitleSuffix = regexp.MustCompile(`\s+(KR|JP|CN|XX)$`)
)

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     "https://flamecomics.xyz",
		allowedHost: []string{"flamecomics.xyz"},
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
		allowedHost = []string{"flamecomics.xyz"}
	}
	return &Connector{baseURL: strings.TrimRight(baseURL, "/"), allowedHost: allowedHost, httpClient: client}
}

func (c *Connector) Key() string {
	return "flamecomics"
}

func (c *Connector) Name() string {
	return "FlameComics"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.baseURL+"/latest")
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
		return nil, fmt.Errorf("url does not belong to flamecomics")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "series" {
		return nil, fmt.Errorf("flamecomics url must match /series/{id}")
	}

	seriesID := strings.TrimSpace(segments[1])
	if !seriesIDPattern.MatchString(seriesID) {
		return nil, fmt.Errorf("invalid flamecomics series id")
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

	body, err := c.fetchPage(ctx, c.baseURL+"/latest")
	if err != nil {
		body, err = c.fetchPage(ctx, c.baseURL+"/")
		if err != nil {
			return nil, fmt.Errorf("fetch flamecomics pages: %w", err)
		}
	}

	entries := c.collectSeriesEntries(body)
	if len(entries) == 0 {
		return []connectors.MangaResult{}, nil
	}

	results := make([]connectors.MangaResult, 0, limit)
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if len(results) >= limit {
			break
		}

		normalizedTitle := strings.ToLower(strings.TrimSpace(entry.Title))
		if normalizedTitle == "" {
			continue
		}
		if !strings.Contains(normalizedTitle, query) {
			continue
		}
		if _, ok := seen[entry.SeriesID]; ok {
			continue
		}

		resolved, resolveErr := c.resolveBySeriesID(ctx, entry.SeriesID)
		if resolveErr != nil {
			continue
		}

		results = append(results, *resolved)
		seen[entry.SeriesID] = struct{}{}
	}

	return results, nil
}

type seriesEntry struct {
	SeriesID string
	Title    string
}

func (c *Connector) collectSeriesEntries(body string) []seriesEntry {
	matches := seriesAnchorPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	entries := make([]seriesEntry, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		seriesID := strings.TrimSpace(match[1])
		if !seriesIDPattern.MatchString(seriesID) {
			continue
		}

		title := cleanText(match[2])
		title = strings.TrimSpace(trailingRegionCodeTitleSuffix.ReplaceAllString(title, ""))
		if title == "" || strings.EqualFold(title, "All Chapters") || strings.HasPrefix(strings.ToLower(title), "chapter ") {
			continue
		}

		entryKey := seriesID + "::" + strings.ToLower(title)
		if _, exists := seen[entryKey]; exists {
			continue
		}
		seen[entryKey] = struct{}{}
		entries = append(entries, seriesEntry{SeriesID: seriesID, Title: title})
	}

	return entries
}

func (c *Connector) resolveBySeriesID(ctx context.Context, seriesID string) (*connectors.MangaResult, error) {
	body, err := c.fetchPage(ctx, c.baseURL+"/series/"+seriesID)
	if err != nil {
		return nil, fmt.Errorf("fetch series page: %w", err)
	}

	title := extractTitle(body)
	if title == "" {
		title = "Series " + seriesID
	}

	coverImageURL := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaImagePattern, body)))
	coverImageURL = normalizeFlameImageURL(coverImageURL)

	latestChapter, latestReleaseAt := extractLatestChapterAndReleaseAt(body, seriesID)

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  seriesID,
		Title:         title,
		URL:           "https://flamecomics.xyz/series/" + seriesID,
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: latestReleaseAt,
	}, nil
}

func extractLatestChapterAndReleaseAt(body string, seriesID string) (*float64, *time.Time) {
	matches := chapterBySeriesPattern.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil, nil
	}

	var latestChapter *float64
	var latestReleaseAt *time.Time

	for _, loc := range matches {
		if len(loc) < 4 {
			continue
		}

		candidateSeriesID := strings.TrimSpace(body[loc[2]:loc[3]])
		if candidateSeriesID != seriesID {
			continue
		}

		segmentStart := loc[0]
		segmentEnd := segmentStart + 1800
		if segmentEnd > len(body) {
			segmentEnd = len(body)
		}
		if segmentStart >= segmentEnd {
			continue
		}

		segment := body[segmentStart:segmentEnd]
		chapterRaw := firstSubmatch(chapterNumberPattern, segment)
		if chapterRaw == "" {
			continue
		}

		parsedChapter, parseChapterErr := strconv.ParseFloat(strings.TrimSpace(chapterRaw), 64)
		if parseChapterErr != nil {
			continue
		}

		parsedDate := parseFlameDate(fullDateTimePattern.FindString(segment))
		if latestChapter == nil || parsedChapter > *latestChapter {
			chapterCopy := parsedChapter
			latestChapter = &chapterCopy
			if parsedDate != nil {
				dateCopy := *parsedDate
				latestReleaseAt = &dateCopy
			} else {
				latestReleaseAt = nil
			}
			continue
		}

		if latestChapter != nil && parsedChapter == *latestChapter && latestReleaseAt == nil && parsedDate != nil {
			dateCopy := *parsedDate
			latestReleaseAt = &dateCopy
		}
	}

	return latestChapter, latestReleaseAt
}

func parseFlameDate(raw string) *time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	layouts := []string{
		"January 2, 2006 3:04 PM",
		"Jan 2, 2006 3:04 PM",
		"January 2, 2006",
		"Jan 2, 2006",
	}

	for _, layout := range layouts {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}

	return nil
}

func extractTitle(body string) string {
	title := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaTitlePattern, body)))
	if title == "" {
		title = strings.TrimSpace(html.UnescapeString(cleanText(firstSubmatch(titleTagPattern, body))))
	}
	if title == "" {
		return ""
	}
	title = strings.ReplaceAll(title, "- Flame Comics", "")
	title = strings.ReplaceAll(title, "| Flame Comics", "")
	title = strings.TrimSpace(title)
	return title
}

func normalizeFlameImageURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "&amp;", "&")
	if parsed, err := url.Parse(trimmed); err == nil {
		if parsed.Host == "flamecomics.xyz" && strings.HasPrefix(parsed.Path, "/_next/image") {
			target := parsed.Query().Get("url")
			decoded, decodeErr := url.QueryUnescape(target)
			if decodeErr == nil && strings.TrimSpace(decoded) != "" {
				return strings.TrimSpace(decoded)
			}
		}
	}
	return trimmed
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
