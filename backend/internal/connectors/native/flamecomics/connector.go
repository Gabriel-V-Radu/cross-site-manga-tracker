package flamecomics

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
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

// site is the connector's identity: the one place its key, name, domains,
// canonical origin and reading tier are written down. The constructors' default
// and the Hosts() the registry routes URLs through read the same list, so the
// two cannot drift apart and leave the site reachable by one path but not the
// other. Subdomains (www., cdn.) are covered by connectors.HostAllowed. Flame
// is an origin scanlator: for its own series chapters appear here before any
// aggregator mirrors them.
var site = connectors.Site{
	SiteKey:   "flamecomics",
	SiteName:  "FlameComics",
	SiteHosts: []string{canonicalHost},
	Home:      canonicalBaseURL,
	Rank:      connectors.ReaderRankOrigin,
}

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

// Connector reads flamecomics.xyz by scraping its series pages. The embedded
// Site supplies Key, Name, Kind, the SiteInfo methods and the URL helpers;
// baseURL is where requests go (the live site, or a test server).
type Connector struct {
	connectors.Site
	baseURL    string
	httpClient *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		Site:       site,
		baseURL:    canonicalBaseURL,
		httpClient: connectors.NewThrottledClient(),
	}
}

// NewConnectorWithOptions points the connector at another base URL (a test
// server), optionally claiming other hosts. A nil client gets the shared
// throttled one, so no caller can construct an unpaced connector by accident;
// tests that want to stay unpaced pass their own.
func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = connectors.NewThrottledClient()
	}
	identity := site
	if len(allowedHost) > 0 {
		identity.SiteHosts = allowedHost
	}
	return &Connector{
		Site:       identity,
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: client,
	}
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.baseURL+"/latest")
	return err
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	seriesID, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return nil, err
	}
	return c.resolveBySeriesID(ctx, seriesID)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
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

	results := make([]connectors.MangaResult, 0, query.Limit)
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if len(results) >= query.Limit {
			break
		}

		if _, ok := seen[entry.SeriesID]; ok {
			continue
		}

		resolved, resolveErr := c.resolveBySeriesID(ctx, entry.SeriesID)
		if resolveErr != nil {
			continue
		}
		if !query.Matches(append([]string{resolved.Title, entry.Title, resolved.SourceItemID}, resolved.RelatedTitles...)...) {
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

	seriesID, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", err
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

		return c.AbsoluteURL(hrefRaw), nil
	}

	return "", fmt.Errorf("chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
}

// parseSeriesURL checks the URL is this site's and reads the numeric series id
// out of its /series/{id} path.
func (c *Connector) parseSeriesURL(rawURL string) (string, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", err
	}

	segments := connectors.PathSegments(parsed)
	if len(segments) < 2 || segments[0] != "series" {
		return "", fmt.Errorf("flamecomics url must match /series/{id}")
	}

	seriesID := strings.TrimSpace(segments[1])
	if !seriesIDPattern.MatchString(seriesID) {
		return "", fmt.Errorf("invalid flamecomics series id")
	}
	return seriesID, nil
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
	relatedTitles := searchutil.RelatedTitles(title, searchutil.ExtractRelatedTitles(body))

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

	var latest connectors.LatestReading
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

		latest.Add(parsedChapter, parseFlameDate(fullDateTimePattern.FindString(segment)))
	}

	return latest.Result()
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

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, error) {
	return connectors.FetchHTML(ctx, c.httpClient, endpoint)
}

// seriesURL is the stored, canonical address of a series: always on the real
// site, never on the base URL requests go to.
func (c *Connector) seriesURL(seriesID string) string {
	return c.HomeURL() + "/series/" + seriesID
}
