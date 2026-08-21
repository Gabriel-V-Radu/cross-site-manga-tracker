package weebcentral

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// WeebCentral is the successor of MangaSee/MangaLife. It serves plain HTML
// with no bot challenge and no request signing (it sits behind Cloudflare, but
// as of 2026-08 a normal browser user-agent is enough). Series pages live at
// /series/{ULID}[/{slug}] and embed the most recent chapters; the complete
// list is a separate HTMX fragment at /series/{ULID}/full-chapter-list. Quick
// search is a form POST to /search/simple?location=main returning an HTML
// fragment of series links.
//
// Chapter entries carry a real per-chapter timestamp (unlike MangaBuddy) and a
// label whose prefix varies by series — "Chapter 455", "Episode 326", even
// "Suggestion 192" — so only the trailing number is meaningful. The catalog is
// English-only, which is exactly what makes its chapter numbers comparable to
// the tracker's: no other-language releases to inflate the count.
const canonicalBaseURL = "https://weebcentral.com"

// maxPlausibleChapter mirrors the MangaBuddy guard: a parsed number beyond
// this is data noise, not a release, and must not inflate a tracker.
const maxPlausibleChapter = 10000

var (
	// ULIDs use Crockford base32, which excludes I, L, O and U.
	seriesPathPattern   = regexp.MustCompile(`(?i)^/series/([0-9A-HJKMNP-TV-Z]{26})(?:/|$)`)
	chapterBlockPattern = regexp.MustCompile(`(?is)<a[^>]+href="(?:https?://[^"/]+)?/chapters/([0-9A-HJKMNP-TV-Z]{26})"[^>]*>(.*?)</a>`)
	chapterLabelPattern = regexp.MustCompile(`(?is)<span class="">([^<]+)</span>`)
	datetimePattern     = regexp.MustCompile(`(?i)datetime="([^"]+)"`)
	trailingNumber      = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*$`)

	searchAnchorPattern = regexp.MustCompile(`(?is)<a[^>]+href="(?:https?://weebcentral\.com)?/series/([0-9A-HJKMNP-TV-Z]{26})/([^"]+)"[^>]*>(.*?)</a>`)
	coverImagePattern   = regexp.MustCompile(`(?i)src="([^"]*cover/[^"]+)"`)

	metaTitlePattern = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	metaImagePattern = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`)

	associatedBlockPattern = regexp.MustCompile(`(?is)Associated Name\(s\)</strong>\s*<ul[^>]*>(.*?)</ul>`)
	listItemPattern        = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)

	htmlTagPattern    = regexp.MustCompile(`(?is)<[^>]+>`)
	whitespacePattern = regexp.MustCompile(`\s+`)
)

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     canonicalBaseURL,
		allowedHost: []string{"weebcentral.com"},
		httpClient:  connectors.NewThrottledClient(12 * time.Second),
	}
}

func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"weebcentral.com"}
	}
	return &Connector{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
	}
}

func (c *Connector) Key() string {
	return "weebcentral"
}

func (c *Connector) Name() string {
	return "WeebCentral"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchSearchFragment(ctx, "one piece")
	return err
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	seriesID, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return nil, err
	}

	body, err := c.fetchPage(ctx, c.seriesURL(seriesID))
	if err != nil {
		return nil, fmt.Errorf("fetch weebcentral series page: %w", err)
	}

	title := extractTitle(body)
	if title == "" {
		return nil, fmt.Errorf("weebcentral series page has no title")
	}

	latestChapter, releaseAt := extractLatestChapter(body)

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  seriesID,
		Title:         title,
		RelatedTitles: extractAssociatedNames(body),
		URL:           c.seriesURL(seriesID),
		CoverImageURL: strings.TrimSpace(html.UnescapeString(firstSubmatch(metaImagePattern, body))),
		LatestChapter: latestChapter,
		LastUpdatedAt: releaseAt,
	}, nil
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

	body, err := c.fetchSearchFragment(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search weebcentral: %w", err)
	}

	matches := searchAnchorPattern.FindAllStringSubmatch(body, -1)
	results := make([]connectors.MangaResult, 0, limit)
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(results) >= limit {
			break
		}
		if len(match) < 4 {
			continue
		}

		seriesID := strings.ToUpper(strings.TrimSpace(match[1]))
		if _, exists := seen[seriesID]; exists {
			continue
		}
		seen[seriesID] = struct{}{}

		slugTitle := strings.ReplaceAll(strings.TrimSpace(match[2]), "-", " ")
		anchorTitle := cleanText(match[3])
		// The anchor's only text node is the title; img alt text lives in
		// attributes and is stripped with the tags.
		resultTitle := anchorTitle
		if resultTitle == "" {
			resultTitle = slugTitle
		}

		if !searchutil.AnyCandidateMatches([]string{anchorTitle, slugTitle}, normalizedQuery, queryTokens) {
			continue
		}

		results = append(results, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  seriesID,
			Title:         resultTitle,
			URL:           c.seriesURL(seriesID),
			CoverImageURL: strings.TrimSpace(html.UnescapeString(firstSubmatch(coverImagePattern, match[3]))),
		})
	}

	return results, nil
}

// ResolveChapterURL finds the page for a specific chapter number. WeebCentral
// chapter URLs are ULIDs with no derivable relation to the number, so the full
// chapter list has to be consulted.
func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if chapter <= 0 || chapter != chapter {
		return "", fmt.Errorf("invalid chapter")
	}

	seriesID, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", err
	}

	body, err := c.fetchPage(ctx, c.seriesURL(seriesID)+"/full-chapter-list")
	if err != nil {
		return "", fmt.Errorf("fetch weebcentral chapter list: %w", err)
	}

	for _, entry := range parseChapterEntries(body) {
		if entry.number == nil {
			continue
		}
		if diff := *entry.number - chapter; diff < 1e-9 && diff > -1e-9 {
			return c.baseURL + "/chapters/" + entry.chapterID, nil
		}
	}

	return "", fmt.Errorf("chapter %s not found on weebcentral", strconv.FormatFloat(chapter, 'f', -1, 64))
}

func (c *Connector) parseSeriesURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return "", fmt.Errorf("url does not belong to weebcentral")
	}

	match := seriesPathPattern.FindStringSubmatch(parsed.Path)
	if len(match) < 2 {
		return "", fmt.Errorf("weebcentral url must match /series/{id}")
	}

	return strings.ToUpper(match[1]), nil
}

func (c *Connector) seriesURL(seriesID string) string {
	return c.baseURL + "/series/" + seriesID
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

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	return c.doRequest(req)
}

func (c *Connector) fetchSearchFragment(ctx context.Context, query string) (string, error) {
	form := url.Values{"text": {strings.TrimSpace(query)}}
	endpoint := c.baseURL + "/search/simple?location=main"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	return c.doRequest(req)
}

func (c *Connector) doRequest(req *http.Request) (string, error) {
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

type chapterEntry struct {
	chapterID  string
	number     *float64
	releasedAt *time.Time
}

// parseChapterEntries reads the chapter anchors out of a series page or a
// full-chapter-list fragment; both use the same markup.
func parseChapterEntries(body string) []chapterEntry {
	matches := chapterBlockPattern.FindAllStringSubmatch(body, -1)
	entries := make([]chapterEntry, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		entry := chapterEntry{chapterID: strings.ToUpper(strings.TrimSpace(match[1]))}
		block := match[2]

		label := cleanText(firstSubmatch(chapterLabelPattern, block))
		if numberRaw := trailingNumber.FindString(label); numberRaw != "" {
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(numberRaw), 64); err == nil && parsed > 0 && parsed < maxPlausibleChapter {
				entry.number = &parsed
			}
		}

		if rawTime := firstSubmatch(datetimePattern, block); rawTime != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if parsed, err := time.Parse(layout, strings.TrimSpace(rawTime)); err == nil {
					utc := parsed.UTC()
					entry.releasedAt = &utc
					break
				}
			}
		}

		entries = append(entries, entry)
	}
	return entries
}

// extractLatestChapter returns the highest chapter number on the page and that
// chapter's own release timestamp. Highest number rather than first entry:
// the list is newest-first today, but a re-uploaded early chapter appearing on
// top must not walk the tracker backwards.
func extractLatestChapter(body string) (*float64, *time.Time) {
	var latest *float64
	var releasedAt *time.Time
	for _, entry := range parseChapterEntries(body) {
		if entry.number == nil {
			continue
		}
		if latest == nil || *entry.number > *latest {
			latest = entry.number
			releasedAt = entry.releasedAt
		}
	}
	return latest, releasedAt
}

func extractTitle(body string) string {
	title := strings.TrimSpace(html.UnescapeString(firstSubmatch(metaTitlePattern, body)))
	title = strings.TrimSuffix(title, "| Weeb Central")
	return strings.TrimSpace(title)
}

func extractAssociatedNames(body string) []string {
	block := firstSubmatch(associatedBlockPattern, body)
	if block == "" {
		return nil
	}

	items := listItemPattern.FindAllStringSubmatch(block, -1)
	names := make([]string, 0, len(items))
	for _, item := range items {
		if len(item) < 2 {
			continue
		}
		if name := cleanText(item[1]); name != "" {
			names = append(names, name)
		}
	}
	return searchutil.FilterEnglishAlphabetNames(names)
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
