package freewebnovel

import (
	"context"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const canonicalBaseURL = "https://freewebnovel.com"

var (
	searchRowSplitPattern    = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*\bli-row\b[^"']*["'][^>]*>`)
	searchTitleAnchorPattern = regexp.MustCompile(`(?is)<h3[^>]*class=["'][^"']*\btit\b[^"']*["'][^>]*>\s*<a[^>]+href=["']/novel/([^"'/?#]+)["'][^>]*>(.*?)</a>`)
	searchImgSrcPattern      = regexp.MustCompile(`(?is)<img[^>]+src=["']([^"']+)["'][^>]*>`)
	chapterHrefPattern       = regexp.MustCompile(`(?is)/novel/[^"'/]+/chapter-([0-9]+)`)

	ogTitlePattern         = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]*content="([^"]*)"`)
	ogImagePattern         = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:image["'][^>]*content="([^"]*)"`)
	novelNamePattern       = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:novel_name["'][^>]*content="([^"]*)"`)
	updateTimePattern      = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:update_time["'][^>]*content="([^"]*)"`)
	latestChapterURLPatt   = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:lastest_chapter_url["'][^>]*content="([^"]*)"`)
	titleHeadingPattern    = regexp.MustCompile(`(?is)<h1[^>]*class=["'][^"']*\btit\b[^"']*["'][^>]*>(.*?)</h1>`)
	alternativeNamesPatt   = regexp.MustCompile(`(?is)title=["']Alternative names["'][^>]*>.*?<div[^>]*class=["'][^"']*\bright\b[^"']*["'][^>]*>\s*<span[^>]*class=["'][^"']*\bs1\b[^"']*["'][^>]*>(.*?)</span>`)
	htmlTagPattern         = regexp.MustCompile(`(?is)<[^>]+>`)
	whitespacePattern      = regexp.MustCompile(`\s+`)
)

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
}

type searchEntry struct {
	Slug          string
	Title         string
	CoverImage    string
	LatestChapter *float64
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     canonicalBaseURL,
		allowedHost: []string{"freewebnovel.com"},
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
		allowedHost = []string{"freewebnovel.com"}
	}
	return &Connector{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
	}
}

func (c *Connector) Key() string {
	return "freewebnovel"
}

func (c *Connector) Name() string {
	return "FreeWebNovel"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.baseURL+"/home")
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
		return nil, fmt.Errorf("url does not belong to freewebnovel")
	}

	slug := extractNovelSlugFromPath(parsed.Path)
	if slug == "" {
		return nil, fmt.Errorf("freewebnovel url must match /novel/{id}")
	}

	return c.resolveBySlug(ctx, slug)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	normalizedQuery := searchutil.Normalize(query)
	queryTokens := searchutil.TokenizeNormalized(normalizedQuery)
	if normalizedQuery == "" || len(queryTokens) == 0 {
		return nil, fmt.Errorf("title is required")
	}

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	body, err := c.fetchPage(ctx, c.baseURL+"/search?keyword="+url.QueryEscape(query))
	if err != nil {
		return nil, fmt.Errorf("fetch freewebnovel search page: %w", err)
	}

	entries := parseSearchEntries(body)
	if len(entries) == 0 {
		return []connectors.MangaResult{}, nil
	}

	results := make([]connectors.MangaResult, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if !searchutil.AnyCandidateMatches([]string{entry.Title, entry.Slug}, normalizedQuery, queryTokens) {
			continue
		}

		result := connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  entry.Slug,
			Title:         entry.Title,
			URL:           c.novelURL(entry.Slug),
			CoverImageURL: c.absoluteURL(entry.CoverImage),
		}

		if entry.LatestChapter != nil {
			latest := *entry.LatestChapter
			result.LatestChapter = &latest
		}

		results = append(results, result)
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if math.IsNaN(chapter) || math.IsInf(chapter, 0) || chapter <= 0 {
		return "", fmt.Errorf("invalid chapter")
	}

	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return "", fmt.Errorf("url does not belong to freewebnovel")
	}

	slug := extractNovelSlugFromPath(parsed.Path)
	if slug == "" {
		return "", fmt.Errorf("freewebnovel url must match /novel/{id}")
	}

	// FreeWebNovel chapter URLs are a deterministic sequential index:
	// /novel/{slug}/chapter-{N}. The tracked chapter number is that same
	// index (taken from og:novel:lastest_chapter_url), so we can build the
	// URL directly without fetching a chapter list.
	return c.novelURL(slug) + "/chapter-" + formatChapterNumber(chapter), nil
}

func (c *Connector) resolveBySlug(ctx context.Context, slug string) (*connectors.MangaResult, error) {
	body, err := c.fetchPage(ctx, c.baseURL+"/novel/"+slug)
	if err != nil {
		return nil, fmt.Errorf("fetch novel page: %w", err)
	}

	title := extractTitle(body, slug)
	relatedTitles := extractRelatedTitles(body, title)

	coverImageURL := c.absoluteURL(strings.TrimSpace(html.UnescapeString(firstSubmatch(ogImagePattern, body))))

	latestChapter := parseLatestChapterFromURL(firstSubmatch(latestChapterURLPatt, body))
	lastUpdatedAt := parseUpdateTime(firstSubmatch(updateTimePattern, body))

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  slug,
		Title:         title,
		RelatedTitles: relatedTitles,
		URL:           c.novelURL(slug),
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: lastUpdatedAt,
	}, nil
}

func parseSearchEntries(body string) []searchEntry {
	// Each search result is wrapped in <div class="li-row">. The block is
	// div-based (no clean closing delimiter), so slice the body on the row
	// markers and parse the first result within each slice.
	markers := searchRowSplitPattern.FindAllStringIndex(body, -1)
	if len(markers) == 0 {
		return nil
	}

	entries := make([]searchEntry, 0, len(markers))
	seen := make(map[string]struct{}, len(markers))
	for index, marker := range markers {
		start := marker[1]
		end := len(body)
		if index+1 < len(markers) {
			end = markers[index+1][0]
		}
		block := body[start:end]

		anchor := searchTitleAnchorPattern.FindStringSubmatch(block)
		if len(anchor) < 3 {
			continue
		}

		slug := strings.TrimSpace(anchor[1])
		if slug == "" {
			continue
		}
		if _, exists := seen[slug]; exists {
			continue
		}

		title := cleanText(anchor[2])
		if title == "" {
			title = prettifySlug(slug)
		}

		coverImageURL := strings.TrimSpace(html.UnescapeString(firstSubmatch(searchImgSrcPattern, block)))

		var latestChapter *float64
		if chapterRaw := firstSubmatch(chapterHrefPattern, block); chapterRaw != "" {
			latestChapter = parseChapterNumber(chapterRaw)
		}

		seen[slug] = struct{}{}
		entries = append(entries, searchEntry{
			Slug:          slug,
			Title:         title,
			CoverImage:    coverImageURL,
			LatestChapter: latestChapter,
		})
	}

	return entries
}

func extractTitle(body string, slug string) string {
	title := strings.TrimSpace(html.UnescapeString(firstSubmatch(ogTitlePattern, body)))
	if title != "" {
		return title
	}

	title = strings.TrimSpace(html.UnescapeString(firstSubmatch(novelNamePattern, body)))
	if title != "" {
		return title
	}

	title = cleanText(firstSubmatch(titleHeadingPattern, body))
	if title != "" {
		return title
	}

	return prettifySlug(slug)
}

func extractRelatedTitles(body string, primaryTitle string) []string {
	candidates := make([]string, 0, 16)

	altRaw := cleanText(firstSubmatch(alternativeNamesPatt, body))
	if altRaw != "" {
		for _, part := range strings.Split(altRaw, ",") {
			candidate := strings.TrimSpace(part)
			if candidate == "" {
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	candidates = append(candidates, searchutil.ExtractRelatedTitles(body)...)
	filtered := searchutil.FilterEnglishAlphabetNames(candidates)
	if len(filtered) == 0 {
		return nil
	}

	primaryKey := searchutil.Normalize(primaryTitle)
	related := make([]string, 0, len(filtered))
	for _, candidate := range filtered {
		if searchutil.Normalize(candidate) == primaryKey {
			continue
		}
		related = append(related, candidate)
	}
	if len(related) == 0 {
		return nil
	}

	return searchutil.UniqueNonEmpty(related)
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

func parseLatestChapterFromURL(raw string) *float64 {
	chapterRaw := firstSubmatch(chapterHrefPattern, strings.TrimSpace(html.UnescapeString(raw)))
	if chapterRaw == "" {
		return nil
	}
	return parseChapterNumber(chapterRaw)
}

func parseChapterNumber(raw string) *float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return nil
	}
	return &value
}

func formatChapterNumber(chapter float64) string {
	return strconv.FormatFloat(chapter, 'f', -1, 64)
}

func parseUpdateTime(raw string) *time.Time {
	normalized := strings.TrimSpace(html.UnescapeString(raw))
	if normalized == "" {
		return nil
	}

	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, normalized)
		if err != nil {
			continue
		}
		utc := parsed.UTC()
		return &utc
	}

	return nil
}

func extractNovelSlugFromPath(rawPath string) string {
	segments := strings.Split(strings.Trim(path.Clean(strings.TrimSpace(rawPath)), "/"), "/")
	if len(segments) < 2 || segments[0] != "novel" {
		return ""
	}

	slug := strings.TrimSpace(segments[1])
	if slug == "" {
		return ""
	}

	return slug
}

func prettifySlug(slug string) string {
	trimmed := strings.TrimSpace(slug)
	if trimmed == "" {
		return "Untitled"
	}

	trimmed = strings.ReplaceAll(trimmed, "-", " ")
	parts := strings.Fields(trimmed)
	for index := range parts {
		if len(parts[index]) == 0 {
			continue
		}
		parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
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
		return canonicalBaseURL + trimmed
	}
	return canonicalBaseURL + "/" + trimmed
}

func (c *Connector) novelURL(slug string) string {
	return canonicalBaseURL + "/novel/" + slug
}
