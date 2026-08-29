package flamecomics

import (
	"context"
	"fmt"
	"html"
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

const (
	canonicalHost    = "flamecomics.xyz"
	canonicalBaseURL = "https://" + canonicalHost
)

// siteHosts is the only place the connector's domains are written down. The
// constructors' default and the Hosts() the registry routes URLs through read
// the same list, so the two cannot drift apart and leave the site reachable by
// one path but not the other. Subdomains (www., cdn.) are covered by
// connectors.HostAllowed.
var siteHosts = []string{canonicalHost}

// flameDateLayouts are the shapes a release date is printed in on the series
// page; the time-less variants exist because older entries carry only the day.
var flameDateLayouts = []string{
	"January 2, 2006 3:04 PM",
	"Jan 2, 2006 3:04 PM",
	"January 2, 2006",
	"Jan 2, 2006",
}

var (
	seriesIDPattern               = regexp.MustCompile(`^\d+$`)
	seriesAnchorPattern           = regexp.MustCompile(`(?is)<a[^>]+href=["'](?:https?://[^"']+)?/series/(\d+)["'][^>]*>(.*?)</a>`)
	metaTitlePattern              = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^\"]+)["']`)
	titleTagPattern               = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	metaImagePattern              = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property=["']og:image["']|name=["']twitter:image["'])[^>]*content=["']([^\"]+)["']`)
	chapterBySeriesPattern        = regexp.MustCompile(`(?i)/series/(\d+)/[a-z0-9]+`)
	chapterAnchorPattern          = regexp.MustCompile(`(?is)<a[^>]+href=["']((?:https?://[^"']+)?/series/(\d+)/[^"']+)["'][^>]*>(.*?)</a>`)
	chapterNumberPattern          = regexp.MustCompile(`(?i)Chapter(?:\s|<!--\s*-->|&nbsp;)+([0-9]+(?:\.[0-9]+)?)`)
	fullDateTimePattern           = regexp.MustCompile(`(?i)(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+\d{1,2},\s+\d{4}(?:\s+\d{1,2}:\d{2}\s*(?:AM|PM))?`)
	trailingRegionCodeTitleSuffix = regexp.MustCompile(`\s+(KR|JP|CN|XX)$`)
)

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     canonicalBaseURL,
		allowedHost: siteHosts,
		httpClient:  connectors.NewThrottledClient(),
	}
}

func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = siteHosts
	}
	return &Connector{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
	}
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
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	normalizedQuery := searchutil.Normalize(query)
	queryTokens := searchutil.TokenizeNormalized(normalizedQuery)
	if normalizedQuery == "" || len(queryTokens) == 0 {
		return nil, fmt.Errorf("title is required")
	}

	limit = connectors.ClampSearchLimit(limit)

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

		if _, ok := seen[entry.SeriesID]; ok {
			continue
		}

		resolved, resolveErr := c.resolveBySeriesID(ctx, entry.SeriesID)
		if resolveErr != nil {
			continue
		}
		if !searchutil.AnyCandidateMatches(
			append([]string{resolved.Title, entry.Title, resolved.SourceItemID}, resolved.RelatedTitles...),
			normalizedQuery,
			queryTokens,
		) {
			continue
		}

		results = append(results, *resolved)
		seen[entry.SeriesID] = struct{}{}
	}

	return results, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapter(chapter) {
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
		return "", fmt.Errorf("url does not belong to flamecomics")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "series" {
		return "", fmt.Errorf("flamecomics url must match /series/{id}")
	}

	seriesID := strings.TrimSpace(segments[1])
	if !seriesIDPattern.MatchString(seriesID) {
		return "", fmt.Errorf("invalid flamecomics series id")
	}

	body, err := c.fetchPage(ctx, c.baseURL+"/series/"+seriesID)
	if err != nil {
		return "", fmt.Errorf("fetch series page: %w", err)
	}

	matches := chapterAnchorPattern.FindAllStringSubmatchIndex(body, -1)
	for _, match := range matches {
		if len(match) < 8 {
			continue
		}

		hrefRaw := strings.TrimSpace(html.UnescapeString(body[match[2]:match[3]]))
		candidateSeriesID := strings.TrimSpace(body[match[4]:match[5]])
		innerHTML := body[match[6]:match[7]]
		if candidateSeriesID != seriesID || hrefRaw == "" {
			continue
		}

		chapterRaw := connectors.FirstSubmatch(chapterNumberPattern, innerHTML)
		if chapterRaw == "" {
			// Some rows print the number next to the anchor rather than inside
			// it, so a short window past the anchor is searched before the
			// entry is given up on.
			segmentEnd := min(match[1]+500, len(body))
			if match[0] < segmentEnd {
				chapterRaw = connectors.FirstSubmatch(chapterNumberPattern, body[match[0]:segmentEnd])
			}
		}
		if chapterRaw == "" {
			continue
		}

		parsedChapter, parseErr := strconv.ParseFloat(strings.TrimSpace(chapterRaw), 64)
		if parseErr != nil || !connectors.ValidChapter(parsedChapter) {
			continue
		}
		if !connectors.SameChapter(parsedChapter, chapter) {
			continue
		}

		return c.absoluteURL(hrefRaw), nil
	}

	return "", fmt.Errorf("chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
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

		title := connectors.CleanText(match[2])
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
	relatedTitles := searchutil.ExtractRelatedTitles(body)
	relatedTitles = removeMatchingTitle(relatedTitles, title)

	coverImageURL := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaImagePattern, body)))
	coverImageURL = normalizeFlameImageURL(coverImageURL)

	latestChapter, latestReleaseAt := extractLatestChapterAndReleaseAt(body, seriesID)

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  seriesID,
		Title:         title,
		RelatedTitles: relatedTitles,
		URL:           c.seriesURL(seriesID),
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
		segmentEnd := min(segmentStart+1800, len(body))
		if segmentStart >= segmentEnd {
			continue
		}

		segment := body[segmentStart:segmentEnd]
		chapterRaw := connectors.FirstSubmatch(chapterNumberPattern, segment)
		if chapterRaw == "" {
			continue
		}

		// The window is wide enough to run past the entry into unrelated
		// markup, so a number that could not be a release is dropped rather
		// than reported as the latest chapter.
		parsedChapter, parseChapterErr := strconv.ParseFloat(strings.TrimSpace(chapterRaw), 64)
		if parseChapterErr != nil || !connectors.ValidChapter(parsedChapter) {
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

		if latestChapter != nil && connectors.SameChapter(parsedChapter, *latestChapter) && latestReleaseAt == nil && parsedDate != nil {
			dateCopy := *parsedDate
			latestReleaseAt = &dateCopy
		}
	}

	return latestChapter, latestReleaseAt
}

func parseFlameDate(raw string) *time.Time {
	return connectors.ParseFirstTime(raw, flameDateLayouts...)
}

func extractTitle(body string) string {
	title := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaTitlePattern, body)))
	if title == "" {
		title = strings.TrimSpace(html.UnescapeString(connectors.CleanText(connectors.FirstSubmatch(titleTagPattern, body))))
	}
	if title == "" {
		return ""
	}
	title = strings.ReplaceAll(title, "- Flame Comics", "")
	title = strings.ReplaceAll(title, "| Flame Comics", "")
	title = strings.TrimSpace(title)
	return title
}

// normalizeFlameImageURL unwraps the site's Next.js image proxy: og:image
// points at /_next/image with the real CDN URL in a query parameter, and the
// stored cover has to be the CDN original, not a resize served by the site.
func normalizeFlameImageURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "&amp;", "&")
	if parsed, err := url.Parse(trimmed); err == nil {
		if parsed.Host == canonicalHost && strings.HasPrefix(parsed.Path, "/_next/image") {
			target := parsed.Query().Get("url")
			decoded, decodeErr := url.QueryUnescape(target)
			if decodeErr == nil && strings.TrimSpace(decoded) != "" {
				return strings.TrimSpace(decoded)
			}
		}
	}
	return trimmed
}

func removeMatchingTitle(values []string, title string) []string {
	if len(values) == 0 {
		return nil
	}

	titleKey := searchutil.Normalize(title)
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		candidateKey := searchutil.Normalize(value)
		if candidateKey == "" {
			continue
		}
		if titleKey != "" && candidateKey == titleKey {
			continue
		}
		filtered = append(filtered, value)
	}

	if len(filtered) == 0 {
		return nil
	}

	return searchutil.UniqueNonEmpty(filtered)
}

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, error) {
	return connectors.FetchHTML(ctx, c.httpClient, endpoint)
}

func (c *Connector) isAllowedHost(host string) bool {
	return connectors.HostAllowed(host, c.allowedHost)
}

// Hosts implements connectors.SiteInfo.
func (c *Connector) Hosts() []string {
	return c.allowedHost
}

// HomeURL implements connectors.SiteInfo.
func (c *Connector) HomeURL() string {
	return c.canonicalBaseURL()
}

// ReaderRank implements connectors.SiteInfo: Flame is an origin scanlator —
// for its own series chapters appear here before any aggregator mirrors them.
func (c *Connector) ReaderRank() int {
	return connectors.ReaderRankOrigin
}

// canonicalBaseURL is the origin every returned or stored URL is built on. It
// is deliberately the production host rather than c.baseURL: c.baseURL is
// where requests go (a test server in tests), while the URLs handed back are
// stored in trackers and opened in the reader's browser, so they must always
// point at the real site.
func (c *Connector) canonicalBaseURL() string {
	return canonicalBaseURL
}

func (c *Connector) seriesURL(seriesID string) string {
	return c.canonicalBaseURL() + "/series/" + seriesID
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
		return c.canonicalBaseURL() + trimmed
	}
	return c.canonicalBaseURL() + "/" + trimmed
}
