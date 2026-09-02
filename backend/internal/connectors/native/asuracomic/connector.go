package asuracomic

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

// defaultBaseURL is the live domain: asuracomic.net answers with a 301 to it
// (verified 2026-09-01) and it is the one NewConnector fetches from.
const defaultBaseURL = "https://asurascans.com"

// site is the connector's identity. asurascans.com is the current domain;
// asuracomic.net is kept because trackers were linked on it before the move, so
// URLs stored in either era still resolve to this connector. Asura is an origin
// scanlator: for its own series chapters appear here before any aggregator
// mirrors them.
var site = connectors.Site{
	SiteKey:   "asuracomic",
	SiteName:  "AsuraComic",
	SiteHosts: []string{"asurascans.com", "asuracomic.net"},
	Home:      defaultBaseURL,
	Rank:      connectors.ReaderRankOrigin,
}

var (
	seriesHrefPattern                = regexp.MustCompile(`(?i)href=["'](?:https?://[^"']+)?/?(?:series|comics)/([a-z0-9-]+)["']`)
	seriesAnchorPattern              = regexp.MustCompile(`(?is)<a[^>]+href=["'](?:https?://[^"']+)?/?(?:series|comics)/([a-z0-9-]+)["'][^>]*>(.*?)</a>`)
	chapterHrefPattern               = regexp.MustCompile(`(?i)(?:/|[a-z0-9-]+/)?chapter/(\d+(?:\.\d+)?)`)
	seriesChapterHrefPattern         = regexp.MustCompile(`(?i)(?:/(?:series|comics)/)?([a-z0-9-]+)/chapter/(\d+(?:\.\d+)?)`)
	chapterPublishedEscPattern       = regexp.MustCompile(`(?is)\\"name\\":\s*(\d+(?:\.\d+)?).*?\\"published_at\\":\\"([^\\"]+)\\"`)
	chapterPublishedRawPattern       = regexp.MustCompile(`(?is)"name":\s*(\d+(?:\.\d+)?).*?"published_at":"([^"]+)"`)
	chapterPublishedEscNumberPattern = regexp.MustCompile(`(?is)\\"number\\":\s*(?:\[0,)?\s*(\d+(?:\.\d+)?)\s*\]?[^\r\n]*?\\"published_at\\":\s*(?:\[0,)?\\"([^\\"]+)\\"\]?`)
	chapterPublishedHTMLPattern      = regexp.MustCompile(`(?is)&quot;number&quot;:\s*\[0,(\d+(?:\.\d+)?)\].*?&quot;published_at&quot;:\s*\[0,&quot;([^&]+)&quot;\]`)
	metaTitlePattern                 = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	titleTagPattern                  = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	metaImagePattern                 = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property=["']og:image["']|name=["']twitter:image["'])[^>]*content=["']([^"']+)["']`)
	renderedCoverPattern             = regexp.MustCompile(`(?is)<img\s+[^>]*src=["']([^"']*asura-images/covers/[^"']+)["'][^>]*>`)
	updatedOnPattern                 = regexp.MustCompile(`(?i)Updated\s+On\s*</[^>]+>\s*<[^>]+>\s*([A-Za-z]+\s+\d{1,2}(?:st|nd|rd|th)?\s+\d{4})`)
	monthDayOrdinalYearPattern       = regexp.MustCompile(`(?i)(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+(\d{1,2})(?:st|nd|rd|th)?\s+(\d{4})`)
)

// Connector reads asurascans.com by scraping its series and browse pages. The
// embedded Site supplies Key, Name, Kind and the SiteInfo methods; baseURL is
// where requests go and, unlike the other scrapers, also the canonical origin
// (see HomeURL).
type Connector struct {
	connectors.Site
	baseURL    string
	httpClient *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		Site:       site,
		baseURL:    defaultBaseURL,
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

// HomeURL is the origin every returned or stored URL is built on. Unlike the
// other scrapers it follows c.baseURL instead of a pinned production constant:
// Asura has already moved domain once, so the base is the single place that
// choice is made, and a stored link must point at the same origin the connector
// verified the chapter on.
func (c *Connector) HomeURL() string {
	return c.baseURL
}

// AbsoluteURL resolves a scraped href against c.baseURL, for the reason HomeURL
// gives.
func (c *Connector) AbsoluteURL(raw string) string {
	return connectors.AbsoluteURL(c.baseURL, raw)
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.searchPageURL("nano"))
	return err
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	seriesID, _, err := c.parseSeriesURL(rawURL)
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

	body, err := c.fetchPage(ctx, c.searchPageURL(query.Raw))
	if err != nil {
		return nil, fmt.Errorf("fetch asuracomic search page: %w", err)
	}

	seriesIDs := collectUniqueSeriesIDs(body)
	if len(seriesIDs) == 0 {
		return []connectors.MangaResult{}, nil
	}

	results := make([]connectors.MangaResult, 0, query.Limit)
	for _, seriesID := range seriesIDs {
		if len(results) >= query.Limit {
			break
		}

		seriesSlugTitle := strings.ReplaceAll(seriesID, "-", " ")
		anchorTitle := extractAnchorTextForSeriesID(body, seriesID)

		resolved, resolveErr := c.resolveBySeriesIDExact(ctx, seriesID)
		if resolveErr != nil {
			continue
		}

		if !query.Matches(resolved.Title, resolved.SourceItemID, anchorTitle, seriesSlugTitle) {
			continue
		}

		results = append(results, *resolved)
	}

	return results, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapter(chapter) {
		return "", fmt.Errorf("invalid chapter")
	}

	seriesID, routeKind, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", err
	}
	if routeKind == "series" {
		canonicalSeriesID, resolveErr := c.findCanonicalSeriesID(ctx, seriesID)
		if resolveErr != nil {
			return "", resolveErr
		}
		seriesID = canonicalSeriesID
	}

	// Verify against the series page rather than constructing blindly: a
	// resolved URL is this connector's claim that the site carries the
	// chapter, and the reader chain ranks sites by that claim. A chapter the
	// page lists as locked early-access counts as carried — its page exists
	// and unlocks on its own timer.
	result, err := c.resolveBySeriesID(ctx, seriesID)
	if err != nil {
		return "", err
	}
	if canonicalID := strings.TrimSpace(result.SourceItemID); canonicalID != "" {
		seriesID = canonicalID
	}
	if result.LatestChapter == nil {
		return "", fmt.Errorf("no chapters listed for %s", seriesID)
	}
	chapterSegment := connectors.FormatChapter(chapter)
	if chapter > *result.LatestChapter {
		return "", fmt.Errorf("chapter %s beyond latest %s: %w",
			chapterSegment,
			connectors.FormatChapter(*result.LatestChapter),
			connectors.ErrChapterNotFound)
	}

	return c.AbsoluteURL("/comics/" + seriesID + "/chapter/" + chapterSegment), nil
}

// parseSeriesURL checks the URL is this site's and reads the series id and the
// route it arrived on ("comics", or the pre-move "series") out of its path.
func (c *Connector) parseSeriesURL(rawURL string) (string, string, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", "", err
	}

	segments := connectors.PathSegments(parsed)
	if len(segments) < 2 {
		return "", "", fmt.Errorf("asuracomic url must match /comics/{id} or /series/{id}")
	}
	routeKind := strings.ToLower(strings.TrimSpace(segments[0]))
	if routeKind != "series" && routeKind != "comics" {
		return "", "", fmt.Errorf("asuracomic url must match /comics/{id} or /series/{id}")
	}

	seriesID := strings.TrimSpace(segments[1])
	if seriesID == "" || !isValidSeriesID(seriesID) {
		return "", "", fmt.Errorf("invalid asuracomic series id")
	}
	return seriesID, routeKind, nil
}

func (c *Connector) resolveBySeriesID(ctx context.Context, seriesID string) (*connectors.MangaResult, error) {
	result, err := c.resolveBySeriesIDExact(ctx, seriesID)
	if err == nil {
		return result, nil
	}
	if !connectors.IsNotFound(err) {
		return nil, err
	}

	canonicalSeriesID, findErr := c.findCanonicalSeriesID(ctx, seriesID)
	if findErr != nil {
		return nil, err
	}
	if canonicalSeriesID == seriesID {
		return nil, err
	}

	return c.resolveBySeriesIDExact(ctx, canonicalSeriesID)
}

func (c *Connector) resolveBySeriesIDExact(ctx context.Context, seriesID string) (*connectors.MangaResult, error) {
	body, err := c.fetchPage(ctx, c.AbsoluteURL("/comics/"+url.PathEscape(seriesID)))
	if err != nil {
		return nil, fmt.Errorf("fetch series page: %w", err)
	}

	title := extractTitle(body)
	if title == "" {
		title = prettifySeriesID(seriesID)
	}
	latestChapter, releaseAtByChapter := extractLatestChapterAndReleaseAt(body, seriesID)
	coverImageURL := c.AbsoluteURL(extractCoverImageURL(body))
	lastUpdatedAt := releaseAtByChapter
	if lastUpdatedAt == nil {
		lastUpdatedAt = extractLastUpdatedAt(body)
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  seriesID,
		Title:         title,
		URL:           c.AbsoluteURL("/comics/" + seriesID),
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: lastUpdatedAt,
	}, nil
}

func (c *Connector) findCanonicalSeriesID(ctx context.Context, seriesID string) (string, error) {
	trimmedSeriesID := strings.TrimSpace(strings.ToLower(seriesID))
	if trimmedSeriesID == "" {
		return "", fmt.Errorf("invalid asuracomic series id")
	}

	query := legacySearchQueryFromSeriesID(trimmedSeriesID)
	if query == "" {
		query = strings.ReplaceAll(trimmedSeriesID, "-", " ")
	}

	body, err := c.fetchPage(ctx, c.searchPageURL(query))
	if err != nil {
		return "", fmt.Errorf("search asuracomic catalog: %w", err)
	}

	seriesIDs := collectUniqueSeriesIDs(body)
	if len(seriesIDs) == 0 {
		return "", fmt.Errorf("series not found in asuracomic catalog")
	}

	targetBase := stripSeriesIDHashSuffix(trimmedSeriesID)
	targetNorm := searchutil.Normalize(strings.ReplaceAll(targetBase, "-", " "))
	for _, candidateID := range seriesIDs {
		if stripSeriesIDHashSuffix(candidateID) == targetBase {
			return candidateID, nil
		}

		candidateTitle := extractAnchorTextForSeriesID(body, candidateID)
		if candidateTitle != "" && searchutil.Normalize(candidateTitle) == targetNorm {
			return candidateID, nil
		}
	}

	return "", fmt.Errorf("series not found in asuracomic catalog")
}

func (c *Connector) searchPageURL(query string) string {
	return c.AbsoluteURL("/browse?page=1&q=" + url.QueryEscape(strings.TrimSpace(query)))
}

func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, error) {
	return connectors.FetchHTML(ctx, c.httpClient, endpoint)
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

// extractAnchorTextForSeriesID returns the first usable link text of an anchor
// pointing at seriesID; the series anchors are matched once with a shared
// pattern and the id compared afterwards.
func extractAnchorTextForSeriesID(body string, seriesID string) string {
	if seriesID == "" {
		return ""
	}
	matches := seriesAnchorPattern.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		if len(match) < 3 || !strings.EqualFold(match[1], seriesID) {
			continue
		}
		candidate := connectors.CleanText(match[2])
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
	title := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaTitlePattern, body)))
	if title == "" {
		title = strings.TrimSpace(html.UnescapeString(connectors.CleanText(connectors.FirstSubmatch(titleTagPattern, body))))
	}
	if title == "" {
		return ""
	}

	title = strings.ReplaceAll(title, "- Asura Scans", "")
	title = strings.ReplaceAll(title, "| Asura Scans", "")
	title = strings.TrimSpace(title)
	return title
}

func extractCoverImageURL(body string) string {
	renderedCover := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(renderedCoverPattern, body)))
	if renderedCover != "" {
		return renderedCover
	}

	return strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaImagePattern, body)))
}

// chapterHrefIndexes locates the chapter links of seriesID in body: each entry
// is [start, end, chapterStart, chapterEnd] of a match. Links of other series
// on the page are skipped; when the page spells its chapter links without the
// series slug at all, every chapter link is taken.
func chapterHrefIndexes(body string, seriesID string) [][]int {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return chapterHrefPattern.FindAllStringSubmatchIndex(body, -1)
	}

	var indexes [][]int
	for _, loc := range seriesChapterHrefPattern.FindAllStringSubmatchIndex(body, -1) {
		if len(loc) < 6 || !strings.EqualFold(body[loc[2]:loc[3]], seriesID) {
			continue
		}
		indexes = append(indexes, []int{loc[0], loc[1], loc[4], loc[5]})
	}
	if len(indexes) == 0 {
		return chapterHrefPattern.FindAllStringSubmatchIndex(body, -1)
	}
	return indexes
}

func extractLatestChapterAndReleaseAt(body string, seriesID string) (*float64, *time.Time) {
	chapterIndexes := chapterHrefIndexes(body, seriesID)
	if len(chapterIndexes) == 0 {
		return nil, nil
	}

	var latest connectors.LatestReading
	for _, loc := range chapterIndexes {
		if len(loc) < 4 {
			continue
		}

		chapterRaw := body[loc[2]:loc[3]]
		parsedChapter, chapterErr := strconv.ParseFloat(strings.TrimSpace(chapterRaw), 64)
		if chapterErr != nil || !connectors.ValidChapter(parsedChapter) {
			continue
		}

		segmentStart := loc[0]
		segmentEnd := min(segmentStart+2200, len(body))
		if segmentStart < 0 || segmentStart >= segmentEnd {
			continue
		}

		segment := body[segmentStart:segmentEnd]
		latest.Add(parsedChapter, parseAsuraDate(monthDayOrdinalYearPattern.FindString(segment)))
	}

	latestChapter, releaseAt := latest.Result()
	if latestChapter == nil {
		return nil, nil
	}
	if publishedAt := extractPublishedAtForChapter(body, *latestChapter); publishedAt != nil {
		return latestChapter, publishedAt
	}
	return latestChapter, releaseAt
}

func extractLastUpdatedAt(body string) *time.Time {
	raw := strings.TrimSpace(connectors.FirstSubmatch(updatedOnPattern, body))
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

	// time.Parse only accepts the canonical month spelling ("February", "Feb")
	// while the page renders months in whatever case its template feels like,
	// so the captured month is lowercased and re-capitalized before parsing.
	normalized := fmt.Sprintf("%s %s %s", connectors.PrettifySlug(strings.ToLower(matches[1])), matches[2], matches[3])
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

func extractPublishedAtForChapter(body string, chapter float64) *time.Time {
	if !connectors.ValidChapter(chapter) {
		return nil
	}

	for _, pattern := range []*regexp.Regexp{chapterPublishedEscPattern, chapterPublishedRawPattern, chapterPublishedEscNumberPattern, chapterPublishedHTMLPattern} {
		matches := pattern.FindAllStringSubmatch(body, -1)
		for _, match := range matches {
			if len(match) < 3 {
				continue
			}

			parsedChapter, err := strconv.ParseFloat(strings.TrimSpace(match[1]), 64)
			if err != nil || !connectors.SameChapter(parsedChapter, chapter) {
				continue
			}

			if parsed := parseAsuraPublishedAt(match[2]); parsed != nil {
				return parsed
			}
		}
	}

	return nil
}

func parseAsuraPublishedAt(raw string) *time.Time {
	return connectors.ParseFirstTime(raw, time.RFC3339Nano, time.RFC3339)
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
	pretty := connectors.PrettifySlug(seriesID)
	if pretty == "" {
		return "Untitled"
	}
	return pretty
}

func legacySearchQueryFromSeriesID(seriesID string) string {
	trimmed := stripSeriesIDHashSuffix(strings.TrimSpace(strings.ToLower(seriesID)))
	trimmed = strings.ReplaceAll(trimmed, "-", " ")
	return strings.Join(strings.Fields(trimmed), " ")
}

func stripSeriesIDHashSuffix(seriesID string) string {
	trimmed := strings.TrimSpace(strings.ToLower(seriesID))
	if trimmed == "" {
		return ""
	}

	lastDash := strings.LastIndex(trimmed, "-")
	if lastDash <= 0 || lastDash >= len(trimmed)-1 {
		return trimmed
	}

	suffix := trimmed[lastDash+1:]
	if !looksLikeHashedSuffix(suffix) {
		return trimmed
	}

	return trimmed[:lastDash]
}

func looksLikeHashedSuffix(suffix string) bool {
	if len(suffix) < 6 || len(suffix) > 12 {
		return false
	}

	hasLetter := false
	hasDigit := false
	for _, r := range suffix {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'f':
			hasLetter = true
		default:
			return false
		}
	}

	return hasLetter && hasDigit
}
