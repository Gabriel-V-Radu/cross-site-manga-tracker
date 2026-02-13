package mangafire

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

var (
	hrefAnyPattern     = regexp.MustCompile(`(?is)<a([^>]*)href=["'](/manga/[^"'#?]+)["']([^>]*)>(.*?)</a>`)
	titleAttrPattern   = regexp.MustCompile(`(?is)\btitle=["']([^"']+)["']`)
	imgSrcPattern      = regexp.MustCompile(`(?is)<img[^>]+src=["']([^"']+)["']`)
	imgAltPattern      = regexp.MustCompile(`(?is)<img[^>]+alt=["']([^"']+)["']`)
	htmlTagPattern     = regexp.MustCompile(`(?is)<[^>]+>`)
	chapterURLPattern  = regexp.MustCompile(`(?i)/chapter-(\d+(?:\.\d+)?)`)
	metaTagPattern     = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	imageTagPattern    = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`)
	posterImagePattern = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*poster[^"']*["'][^>]*>.*?<img[^>]+src=["']([^"']+)["']`)
	sitemapLocPattern  = regexp.MustCompile(`(?is)<loc>([^<]+)</loc>`)
)

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client

	indexMu        sync.RWMutex
	cachedMangaIDs []string
	cachedIndexAt  time.Time
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     "https://mangafire.to",
		allowedHost: []string{"mangafire.to"},
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
		allowedHost = []string{"mangafire.to"}
	}
	return &Connector{baseURL: strings.TrimRight(baseURL, "/"), allowedHost: allowedHost, httpClient: client}
}

func (c *Connector) Key() string {
	return "mangafire"
}

func (c *Connector) Name() string {
	return "MangaFire"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.baseURL+"/home")
	if err != nil {
		return err
	}
	return nil
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
		return nil, fmt.Errorf("url does not belong to mangafire")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "manga" {
		return nil, fmt.Errorf("mangafire url must match /manga/{id}")
	}

	sourceItemID := strings.TrimSpace(segments[1])
	if sourceItemID == "" {
		return nil, fmt.Errorf("invalid mangafire manga id")
	}

	body, err := c.fetchPage(ctx, c.baseURL+"/manga/"+url.PathEscape(sourceItemID))
	if err != nil {
		return nil, fmt.Errorf("fetch manga page: %w", err)
	}

	title := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaTagPattern, body)))
	title = sanitizeTitle(title)
	if title == "" {
		title = prettifyItemID(sourceItemID)
	}

	coverImageURL := strings.TrimSpace(html.UnescapeString(firstSubmatch(imageTagPattern, body)))
	if coverImageURL == "" {
		coverImageURL = strings.TrimSpace(html.UnescapeString(firstSubmatch(posterImagePattern, body)))
	}
	coverImageURL = c.absoluteURL(coverImageURL)
	latestChapter := extractLatestChapter(body)

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  sourceItemID,
		Title:         title,
		URL:           "https://mangafire.to/manga/" + sourceItemID,
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: time.Now().UTC(),
	}, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(strings.ToLower(title))
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	queryTokens := strings.Fields(normalizeForSearch(query))
	significantQueryTokens := filterQueryTokens(queryTokens)

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	searchURL := c.baseURL + "/filter?keyword=" + url.QueryEscape(query)
	body, err := c.fetchPage(ctx, searchURL)
	if err != nil {
		body, err = c.fetchPage(ctx, c.baseURL+"/home")
		if err != nil {
			if isHTTPStatusError(err, http.StatusTooManyRequests) {
				results, fallbackErr := c.appendSitemapMatches(ctx, nil, query, queryTokens, significantQueryTokens, limit, map[string]struct{}{})
				if fallbackErr != nil {
					return []connectors.MangaResult{}, nil
				}
				return results, nil
			}
			return nil, fmt.Errorf("fetch mangafire pages: %w", err)
		}
	}

	entries := parseSearchEntries(body)
	results := make([]connectors.MangaResult, 0, len(entries))
	resultIDs := map[string]struct{}{}
	for _, entry := range entries {
		if !matchesSearchQuery(entry, query, queryTokens, significantQueryTokens) {
			continue
		}

		results = append(results, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  entry.ItemID,
			Title:         entry.Title,
			URL:           "https://mangafire.to/manga/" + entry.ItemID,
			CoverImageURL: c.absoluteURL(entry.CoverImageURL),
			LatestChapter: nil,
			LastUpdatedAt: time.Now().UTC(),
		})
		resultIDs[entry.ItemID] = struct{}{}

		if len(results) >= limit {
			break
		}
	}

	if len(results) < limit {
		updatedResults, fallbackErr := c.appendSitemapMatches(ctx, results, query, queryTokens, significantQueryTokens, limit, resultIDs)
		if fallbackErr == nil {
			results = updatedResults
		}
	}

	for index := range results {
		c.enrichSearchResult(ctx, &results[index])
	}

	return results, nil
}

func (c *Connector) appendSitemapMatches(ctx context.Context, baseResults []connectors.MangaResult, query string, queryTokens []string, significantQueryTokens []string, limit int, existing map[string]struct{}) ([]connectors.MangaResult, error) {
	remaining := limit - len(baseResults)
	if remaining <= 0 {
		return baseResults, nil
	}

	candidateLimit := remaining * 6
	if candidateLimit < 12 {
		candidateLimit = 12
	}
	if candidateLimit > 120 {
		candidateLimit = 120
	}

	fallbackEntries, err := c.searchEntriesFromSitemap(ctx, query, candidateLimit, existing)
	if err != nil {
		return baseResults, err
	}

	results := baseResults
	for _, entry := range fallbackEntries {
		candidate := connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  entry.ItemID,
			Title:         entry.Title,
			URL:           "https://mangafire.to/manga/" + entry.ItemID,
			CoverImageURL: c.absoluteURL(entry.CoverImageURL),
			LatestChapter: nil,
			LastUpdatedAt: time.Now().UTC(),
		}
		c.enrichSearchResult(ctx, &candidate)

		if !matchesSearchQuery(searchEntry{ItemID: candidate.SourceItemID, Title: candidate.Title}, query, queryTokens, significantQueryTokens) {
			continue
		}
		if _, seen := existing[candidate.SourceItemID]; seen {
			continue
		}

		results = append(results, candidate)
		existing[candidate.SourceItemID] = struct{}{}
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

func (c *Connector) enrichSearchResult(ctx context.Context, result *connectors.MangaResult) {
	body, fetchErr := c.fetchPage(ctx, c.baseURL+"/manga/"+url.PathEscape(result.SourceItemID))
	if fetchErr != nil {
		return
	}
	resolvedTitle := sanitizeTitle(strings.TrimSpace(html.UnescapeString(firstSubmatch(metaTagPattern, body))))
	if resolvedTitle != "" {
		result.Title = resolvedTitle
	}
	if latest := extractLatestChapter(body); latest != nil {
		result.LatestChapter = latest
	}
	if result.CoverImageURL == "" {
		result.CoverImageURL = strings.TrimSpace(html.UnescapeString(firstSubmatch(imageTagPattern, body)))
		if result.CoverImageURL == "" {
			result.CoverImageURL = strings.TrimSpace(html.UnescapeString(firstSubmatch(posterImagePattern, body)))
		}
		result.CoverImageURL = c.absoluteURL(result.CoverImageURL)
	}
}

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, error) {
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", c.baseURL+"/home")

		res, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("request failed: %w", err)
		}

		if res.StatusCode >= 200 && res.StatusCode < 300 {
			rawBody, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if readErr != nil {
				return "", fmt.Errorf("read response body: %w", readErr)
			}
			return string(rawBody), nil
		}

		statusErr := &httpStatusError{StatusCode: res.StatusCode}
		retryAfter := res.Header.Get("Retry-After")
		res.Body.Close()

		if res.StatusCode == http.StatusTooManyRequests && attempt < maxAttempts-1 {
			delay := computeRetryDelay(attempt, retryAfter)
			if delay > 0 {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(delay):
				}
			}
			continue
		}

		return "", statusErr
	}

	return "", &httpStatusError{StatusCode: http.StatusTooManyRequests}
}

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("unexpected status: %d", e.StatusCode)
}

func isHTTPStatusError(err error, statusCode int) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == statusCode
}

func computeRetryDelay(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil {
			if seconds < 0 {
				seconds = 0
			}
			if seconds > 4 {
				seconds = 4
			}
			return time.Duration(seconds) * time.Second
		}
	}

	switch attempt {
	case 0:
		return 350 * time.Millisecond
	case 1:
		return 800 * time.Millisecond
	default:
		return 1500 * time.Millisecond
	}
}

type searchEntry struct {
	ItemID        string
	Title         string
	CoverImageURL string
}

func parseSearchEntries(body string) []searchEntry {
	entriesByID := map[string]searchEntry{}

	for _, match := range hrefAnyPattern.FindAllStringSubmatch(body, -1) {
		itemID := normalizeMangaPath(match[2])
		if itemID == "" {
			continue
		}

		attrs := strings.TrimSpace(match[1] + " " + match[3])
		inner := strings.TrimSpace(match[4])

		title := extractSearchTitle(attrs, inner)
		coverImageURL := strings.TrimSpace(html.UnescapeString(firstSubmatch(imgSrcPattern, inner)))

		existing, found := entriesByID[itemID]
		if !found {
			existing = searchEntry{ItemID: itemID}
		}
		if existing.Title == "" && title != "" {
			existing.Title = title
		}
		if existing.CoverImageURL == "" && coverImageURL != "" {
			existing.CoverImageURL = coverImageURL
		}
		entriesByID[itemID] = existing
	}

	entries := make([]searchEntry, 0, len(entriesByID))
	for _, item := range entriesByID {
		if item.Title == "" {
			item.Title = prettifyItemID(item.ItemID)
		}
		entries = append(entries, item)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Title < entries[j].Title
	})

	return entries
}

func extractSearchTitle(attrs string, inner string) string {
	text := strings.TrimSpace(html.UnescapeString(htmlTagPattern.ReplaceAllString(inner, " ")))
	text = strings.Join(strings.Fields(text), " ")
	if text != "" {
		return text
	}

	attrTitle := strings.TrimSpace(html.UnescapeString(firstSubmatch(titleAttrPattern, attrs)))
	if attrTitle != "" {
		return attrTitle
	}

	alt := strings.TrimSpace(html.UnescapeString(firstSubmatch(imgAltPattern, inner)))
	if alt != "" {
		return alt
	}

	return ""
}

func normalizeMangaPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "/")
	if strings.HasPrefix(trimmed, "manga/") {
		return strings.TrimPrefix(trimmed, "manga/")
	}
	return ""
}

func extractLatestChapter(body string) *float64 {
	matches := chapterURLPattern.FindAllStringSubmatch(body, -1)
	var latest *float64
	for _, match := range matches {
		parsed, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		if latest == nil || parsed > *latest {
			value := parsed
			latest = &value
		}
	}
	return latest
}

func firstSubmatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func prettifyItemID(itemID string) string {
	slug := itemID
	if dot := strings.IndexRune(itemID, '.'); dot > 0 {
		slug = itemID[:dot]
	}
	slug = strings.ReplaceAll(slug, "-", " ")
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return itemID
	}
	parts := strings.Fields(slug)
	for index := range parts {
		if parts[index] == "" {
			continue
		}
		parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
	}
	return strings.Join(parts, " ")
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
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	base, err := url.Parse(c.baseURL + "/")
	if err != nil {
		return trimmed
	}
	return base.ResolveReference(parsed).String()
}

func (c *Connector) searchEntriesFromSitemap(ctx context.Context, query string, remaining int, existing map[string]struct{}) ([]searchEntry, error) {
	if remaining <= 0 {
		return nil, nil
	}

	allIDs, err := c.getMangaIDsFromSitemaps(ctx)
	if err != nil {
		return nil, err
	}

	tokens := strings.Fields(normalizeForSearch(query))
	significantTokens := filterQueryTokens(tokens)
	if len(significantTokens) > 0 {
		tokens = significantTokens
	}
	if len(tokens) == 0 {
		return nil, nil
	}

	entries := make([]searchEntry, 0, remaining)
	appended := map[string]struct{}{}

	appendEntry := func(itemID string) bool {
		if _, seen := existing[itemID]; seen {
			return false
		}
		if _, seen := appended[itemID]; seen {
			return false
		}
		entries = append(entries, searchEntry{
			ItemID: itemID,
			Title:  prettifyItemID(itemID),
		})
		appended[itemID] = struct{}{}
		return true
	}

	for _, itemID := range allIDs {
		if !matchesTokens(itemID, tokens) {
			continue
		}
		_ = appendEntry(itemID)
		if len(entries) >= remaining {
			break
		}
	}

	if len(entries) < remaining {
		for _, itemID := range allIDs {
			if !matchesAnyToken(itemID, tokens) {
				continue
			}
			if !appendEntry(itemID) {
				continue
			}
			if len(entries) >= remaining {
				break
			}
		}
	}

	return entries, nil
}

func (c *Connector) getMangaIDsFromSitemaps(ctx context.Context) ([]string, error) {
	c.indexMu.RLock()
	if len(c.cachedMangaIDs) > 0 && time.Since(c.cachedIndexAt) < 30*time.Minute {
		cached := make([]string, len(c.cachedMangaIDs))
		copy(cached, c.cachedMangaIDs)
		c.indexMu.RUnlock()
		return cached, nil
	}
	c.indexMu.RUnlock()

	indexBody, err := c.fetchPage(ctx, c.baseURL+"/sitemap.xml")
	if err != nil {
		return nil, err
	}

	sitemapLinks := make([]string, 0)
	for _, match := range sitemapLocPattern.FindAllStringSubmatch(indexBody, -1) {
		candidate := strings.TrimSpace(match[1])
		if strings.Contains(candidate, "/sitemap-list-") {
			sitemapLinks = append(sitemapLinks, candidate)
		}
	}

	if len(sitemapLinks) == 0 {
		return nil, fmt.Errorf("no sitemap list links found")
	}

	uniqueIDs := map[string]struct{}{}
	for _, sitemapLink := range sitemapLinks {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		body, fetchErr := c.fetchPage(ctx, sitemapLink)
		if fetchErr != nil {
			continue
		}

		for _, match := range sitemapLocPattern.FindAllStringSubmatch(body, -1) {
			link := strings.TrimSpace(match[1])
			parsed, parseErr := url.Parse(link)
			if parseErr != nil {
				continue
			}
			itemID := normalizeMangaPath(parsed.Path)
			if itemID == "" {
				continue
			}
			uniqueIDs[itemID] = struct{}{}
		}
	}

	ids := make([]string, 0, len(uniqueIDs))
	for itemID := range uniqueIDs {
		ids = append(ids, itemID)
	}
	sort.Strings(ids)

	c.indexMu.Lock()
	c.cachedMangaIDs = make([]string, len(ids))
	copy(c.cachedMangaIDs, ids)
	c.cachedIndexAt = time.Now().UTC()
	c.indexMu.Unlock()

	return ids, nil
}

func normalizeForSearch(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("-", " ", ".", " ", "_", " ", ",", " ", ":", " ", ";", " ", "!", " ", "?", " ", "(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ", "'", " ", "\"", " ", "/", " ", "\\", " ")
	clean = replacer.Replace(clean)
	return strings.Join(strings.Fields(clean), " ")
}

func matchesTokens(itemID string, tokens []string) bool {
	base := itemID
	if dot := strings.IndexRune(base, '.'); dot > 0 {
		base = base[:dot]
	}
	normalized := normalizeForSearch(base)
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if !strings.Contains(normalized, token) {
			return false
		}
	}
	return true
}

func matchesAnyToken(itemID string, tokens []string) bool {
	base := itemID
	if dot := strings.IndexRune(base, '.'); dot > 0 {
		base = base[:dot]
	}
	normalized := normalizeForSearch(base)
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func filterQueryTokens(tokens []string) []string {
	stopWords := map[string]struct{}{
		"a":   {},
		"an":  {},
		"my":  {},
		"of":  {},
		"the": {},
		"to":  {},
	}
	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, stop := stopWords[token]; stop {
			continue
		}
		filtered = append(filtered, token)
	}
	return filtered
}

func matchesSearchQuery(entry searchEntry, rawQuery string, queryTokens []string, significantQueryTokens []string) bool {
	normalizedTitle := normalizeForSearch(entry.Title)
	normalizedQuery := normalizeForSearch(rawQuery)

	if normalizedQuery != "" && strings.Contains(normalizedTitle, normalizedQuery) {
		return true
	}

	if len(queryTokens) > 0 {
		if matchesTokens(entry.Title, queryTokens) || matchesTokens(entry.ItemID, queryTokens) {
			return true
		}
	}

	if len(significantQueryTokens) > 0 {
		if matchesTokens(entry.Title, significantQueryTokens) || matchesTokens(entry.ItemID, significantQueryTokens) {
			return true
		}
	}

	return false
}

func sanitizeTitle(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimSuffix(trimmed, " Manga - Read Manga Online Free")
	trimmed = strings.TrimSuffix(trimmed, " - Read Manga Online Free")
	return strings.TrimSpace(trimmed)
}
