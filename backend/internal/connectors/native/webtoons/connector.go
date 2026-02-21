package webtoons

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

var (
	canonicalPattern    = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']canonical["'][^>]*href=["']([^"']+)["']`)
	metaTitlePattern    = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	metaImagePattern    = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`)
	titleHeadingPattern = regexp.MustCompile(`(?is)<h1[^>]*class=["'][^"']*subj[^"']*["'][^>]*>(.*?)</h1>`)
	htmlTagPattern      = regexp.MustCompile(`(?is)<[^>]+>`)
	whitespacePattern   = regexp.MustCompile(`\s+`)
	episodeItemPattern  = regexp.MustCompile(`(?is)<li[^>]*class=["'][^"']*_episodeItem[^"']*["'][^>]*data-episode-no=["'](\d+)["'][^>]*>(.*?)</li>`)
	episodeHrefPattern  = regexp.MustCompile(`(?is)href=["']([^"']*episode_no=\d+[^"']*)["']`)
	episodeDatePattern  = regexp.MustCompile(`(?is)<span[^>]*class=["'][^"']*date[^"']*["'][^>]*>([^<]+)</span>`)
)

type Connector struct {
	baseURL      string
	searchLocale string
	allowedHost  []string
	imageBaseURL string
	httpClient   *http.Client
}

type immediateSearchResponse struct {
	Result struct {
		SearchedList []struct {
			TitleNo         int      `json:"titleNo"`
			Title           string   `json:"title"`
			ThumbnailMobile string   `json:"thumbnailMobile"`
			AuthorNameList  []string `json:"authorNameList"`
			RepresentGenre  string   `json:"representGenre"`
			SearchMode      string   `json:"searchMode"`
		} `json:"searchedList"`
	} `json:"result"`
	Success bool `json:"success"`
}

type episodeEntry struct {
	Number  int
	URL     string
	DateRaw string
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:      "https://www.webtoons.com",
		searchLocale: "en",
		allowedHost:  []string{"webtoons.com"},
		imageBaseURL: "https://swebtoon-phinf.pstatic.net",
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
		allowedHost = []string{"webtoons.com"}
	}
	return &Connector{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		searchLocale: "en",
		allowedHost:  allowedHost,
		imageBaseURL: "https://swebtoon-phinf.pstatic.net",
		httpClient:   client,
	}
}

func (c *Connector) Key() string {
	return "webtoons"
}

func (c *Connector) Name() string {
	return "WEBTOON"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.searchImmediate(ctx, "webtoon")
	if err != nil {
		return err
	}
	return nil
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	parsedURL, err := c.validateAndParseURL(rawURL)
	if err != nil {
		return nil, err
	}

	titleNo, err := extractTitleNo(parsedURL)
	if err != nil {
		return nil, err
	}

	return c.resolveByTitleNo(ctx, titleNo)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	normalizedQuery := searchutil.Normalize(query)
	queryTokens := searchutil.Tokenize(query)
	if normalizedQuery == "" || len(queryTokens) == 0 {
		return nil, fmt.Errorf("title is required")
	}

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	payload, err := c.searchImmediate(ctx, query)
	if err != nil {
		return nil, err
	}

	seen := make(map[int]struct{}, len(payload.Result.SearchedList))
	results := make([]connectors.MangaResult, 0, min(limit, len(payload.Result.SearchedList)))
	for _, item := range payload.Result.SearchedList {
		if !strings.EqualFold(strings.TrimSpace(item.SearchMode), "TITLE") {
			continue
		}
		if !searchutil.AnyCandidateMatches([]string{item.Title}, normalizedQuery, queryTokens) {
			continue
		}
		if item.TitleNo <= 0 {
			continue
		}
		if _, ok := seen[item.TitleNo]; ok {
			continue
		}

		sourceItemID := strconv.Itoa(item.TitleNo)
		result := connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  sourceItemID,
			Title:         strings.TrimSpace(item.Title),
			URL:           c.baseURL + "/episodeList?titleNo=" + sourceItemID,
			CoverImageURL: c.absoluteImageURL(item.ThumbnailMobile),
		}

		// Enrich with latest episode/date to improve tracker auto-fill reliability.
		resolveCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		resolved, resolveErr := c.resolveByTitleNo(resolveCtx, item.TitleNo)
		cancel()
		if resolveErr == nil && resolved != nil {
			if strings.TrimSpace(resolved.Title) != "" {
				result.Title = resolved.Title
			}
			if strings.TrimSpace(resolved.URL) != "" {
				result.URL = resolved.URL
			}
			if strings.TrimSpace(resolved.CoverImageURL) != "" {
				result.CoverImageURL = resolved.CoverImageURL
			}
			result.LatestChapter = resolved.LatestChapter
			result.LastUpdatedAt = resolved.LastUpdatedAt
		}

		results = append(results, result)
		seen[item.TitleNo] = struct{}{}

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	episodeNo, err := parseEpisodeNumber(chapter)
	if err != nil {
		return "", err
	}

	parsedURL, err := c.validateAndParseURL(rawURL)
	if err != nil {
		return "", err
	}

	titleNo, err := extractTitleNo(parsedURL)
	if err != nil {
		return "", err
	}

	pageOneEntries, err := c.fetchEpisodeListEntries(ctx, titleNo, 1)
	if err != nil {
		return "", err
	}
	if len(pageOneEntries) == 0 {
		return "", fmt.Errorf("webtoons episode list is empty")
	}

	if entry := findEpisodeEntry(pageOneEntries, episodeNo); entry != nil && strings.TrimSpace(entry.URL) != "" {
		return strings.TrimSpace(entry.URL), nil
	}

	latestEpisode := findLatestEpisodeNumber(pageOneEntries)
	if episodeNo > latestEpisode {
		return "", fmt.Errorf("episode %d not found", episodeNo)
	}

	page := ((latestEpisode - episodeNo) / 10) + 1
	if page < 1 {
		page = 1
	}

	if page > 1 {
		pageEntries, fetchErr := c.fetchEpisodeListEntries(ctx, titleNo, page)
		if fetchErr == nil {
			if entry := findEpisodeEntry(pageEntries, episodeNo); entry != nil && strings.TrimSpace(entry.URL) != "" {
				return strings.TrimSpace(entry.URL), nil
			}
		}
	}

	return "", fmt.Errorf("episode %d not found", episodeNo)
}

func (c *Connector) validateAndParseURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("url is required")
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsedURL.Hostname()) {
		return nil, fmt.Errorf("url does not belong to webtoons")
	}

	return parsedURL, nil
}

func (c *Connector) searchImmediate(ctx context.Context, query string) (*immediateSearchResponse, error) {
	endpoint := c.baseURL + "/" + c.searchLocale + "/search/immediate?keyword=" + url.QueryEscape(strings.TrimSpace(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request search: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("webtoons search returned status %d", res.StatusCode)
	}

	var payload immediateSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	if !payload.Success {
		return nil, fmt.Errorf("webtoons search was not successful")
	}

	return &payload, nil
}

func (c *Connector) resolveByTitleNo(ctx context.Context, titleNo int) (*connectors.MangaResult, error) {
	endpoint := c.episodeListURL(titleNo, 1)
	body, finalURL, err := c.fetchPage(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	canonicalURL := strings.TrimSpace(html.UnescapeString(firstSubmatch(canonicalPattern, body)))
	if canonicalURL == "" {
		canonicalURL = strings.TrimSpace(finalURL)
	}
	if canonicalURL == "" {
		canonicalURL = endpoint
	}

	title := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaTitlePattern, body)))
	if title == "" {
		title = strings.TrimSpace(html.UnescapeString(cleanText(firstSubmatch(titleHeadingPattern, body))))
	}
	if title == "" {
		title = "WEBTOON " + strconv.Itoa(titleNo)
	}

	coverImageURL := c.absoluteURL(strings.TrimSpace(html.UnescapeString(firstSubmatch(metaImagePattern, body))))

	entries := extractEpisodeEntries(body, c.baseURL)
	latestEpisodeNo := findLatestEpisodeNumber(entries)
	var latestChapter *float64
	var latestUpdatedAt *time.Time
	if latestEpisodeNo > 0 {
		latestValue := float64(latestEpisodeNo)
		latestChapter = &latestValue
		if latestEntry := findEpisodeEntry(entries, latestEpisodeNo); latestEntry != nil {
			latestUpdatedAt = parseWebtoonsDate(latestEntry.DateRaw)
		}
	}

	sourceItemID := strconv.Itoa(titleNo)
	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  sourceItemID,
		Title:         title,
		URL:           canonicalURL,
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: latestUpdatedAt,
	}, nil
}

func (c *Connector) fetchEpisodeListEntries(ctx context.Context, titleNo int, page int) ([]episodeEntry, error) {
	endpoint := c.episodeListURL(titleNo, page)
	body, _, err := c.fetchPage(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return extractEpisodeEntries(body, c.baseURL), nil
}

func (c *Connector) episodeListURL(titleNo int, page int) string {
	values := url.Values{}
	values.Set("titleNo", strconv.Itoa(titleNo))
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	return c.baseURL + "/episodeList?" + values.Encode()
}

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", "", fmt.Errorf("webtoons returned status %d", res.StatusCode)
	}

	rawBody, err := io.ReadAll(res.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response body: %w", err)
	}

	finalURL := endpoint
	if res.Request != nil && res.Request.URL != nil {
		finalURL = res.Request.URL.String()
	}

	return string(rawBody), finalURL, nil
}

func (c *Connector) isAllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, allowed := range c.allowedHost {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
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

func (c *Connector) absoluteImageURL(raw string) string {
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
		return c.imageBaseURL + trimmed
	}
	return c.imageBaseURL + "/" + trimmed
}

func extractTitleNo(parsedURL *url.URL) (int, error) {
	if parsedURL == nil {
		return 0, fmt.Errorf("invalid webtoons url")
	}

	titleRaw := strings.TrimSpace(parsedURL.Query().Get("title_no"))
	if titleRaw == "" {
		titleRaw = strings.TrimSpace(parsedURL.Query().Get("titleNo"))
	}
	if titleRaw == "" {
		return 0, fmt.Errorf("webtoons url must include title_no or titleNo")
	}

	titleNo, err := strconv.Atoi(titleRaw)
	if err != nil || titleNo <= 0 {
		return 0, fmt.Errorf("invalid webtoons title number")
	}

	return titleNo, nil
}

func parseEpisodeNumber(chapter float64) (int, error) {
	if math.IsNaN(chapter) || math.IsInf(chapter, 0) || chapter <= 0 {
		return 0, fmt.Errorf("invalid chapter")
	}

	rounded := math.Round(chapter)
	if math.Abs(chapter-rounded) > 1e-9 {
		return 0, fmt.Errorf("webtoons chapter must be a whole episode number")
	}

	episode := int(rounded)
	if episode <= 0 {
		return 0, fmt.Errorf("invalid chapter")
	}
	return episode, nil
}

func extractEpisodeEntries(body string, baseURL string) []episodeEntry {
	matches := episodeItemPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	entries := make([]episodeEntry, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		number, err := strconv.Atoi(strings.TrimSpace(match[1]))
		if err != nil || number <= 0 {
			continue
		}

		block := match[2]
		href := strings.TrimSpace(html.UnescapeString(firstSubmatch(episodeHrefPattern, block)))
		dateRaw := strings.TrimSpace(html.UnescapeString(firstSubmatch(episodeDatePattern, block)))

		entry := episodeEntry{
			Number:  number,
			URL:     toAbsoluteURL(baseURL, href),
			DateRaw: dateRaw,
		}
		entries = append(entries, entry)
	}

	return entries
}

func findEpisodeEntry(entries []episodeEntry, episodeNo int) *episodeEntry {
	for index := range entries {
		if entries[index].Number == episodeNo {
			return &entries[index]
		}
	}
	return nil
}

func findLatestEpisodeNumber(entries []episodeEntry) int {
	latest := 0
	for _, entry := range entries {
		if entry.Number > latest {
			latest = entry.Number
		}
	}
	return latest
}

func parseWebtoonsDate(raw string) *time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	layouts := []string{
		"Jan 2, 2006",
		"January 2, 2006",
		"2006-01-02",
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

func firstSubmatch(pattern *regexp.Regexp, raw string) string {
	matches := pattern.FindStringSubmatch(raw)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func cleanText(raw string) string {
	text := htmlTagPattern.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	text = whitespacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func toAbsoluteURL(baseURL string, raw string) string {
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
		return strings.TrimRight(baseURL, "/") + trimmed
	}
	return strings.TrimRight(baseURL, "/") + "/" + trimmed
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
