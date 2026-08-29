package mgeko

import (
	"context"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const canonicalBaseURL = "https://www.mgeko.cc"

var (
	novelItemPattern         = regexp.MustCompile(`(?is)<li[^>]*class=["'][^"']*novel-item[^"']*["'][^>]*>(.*?)</li>`)
	mangaHrefPattern         = regexp.MustCompile(`(?is)<a[^>]+href=["'](/manga/[^"'/?#]+/?)["'][^>]*`)
	novelTitlePattern        = regexp.MustCompile(`(?is)<h4[^>]*class=["'][^"']*novel-title[^"']*["'][^>]*>(.*?)</h4>`)
	anchorTitleAttrPattern   = regexp.MustCompile(`(?is)<a[^>]+title=["']([^"']+)["'][^>]*>`)
	imgDataSrcPattern        = regexp.MustCompile(`(?is)<img[^>]+data-src=["']([^"']+)["'][^>]*>`)
	imgSrcPattern            = regexp.MustCompile(`(?is)<img[^>]+src=["']([^"']+)["'][^>]*>`)
	searchChapterPattern     = regexp.MustCompile(`(?is)<strong[^>]*>\s*Chapters?\s*([0-9]+(?:-[0-9]+)?)`)
	searchUpdatedPattern     = regexp.MustCompile(`(?is)<span[^>]*>\s*<i[^>]*fa-clock[^>]*>.*?</i>\s*([^<]+?)(?:\s+Ago)?\s*</span>`)
	titleHeadingPattern      = regexp.MustCompile(`(?is)<h1[^>]*class=["'][^"']*novel-title[^"']*["'][^>]*>(.*?)</h1>`)
	altTitleHeadingPattern   = regexp.MustCompile(`(?is)<h2[^>]*class=["'][^"']*alternative-title[^"']*["'][^>]*>(.*?)</h2>`)
	metaTitlePattern         = regexp.MustCompile(`(?is)<meta\s+[^>]*name=["']title["'][^>]*content=["']([^"']+)["']`)
	ogImagePattern           = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`)
	coverDataSrcPattern      = regexp.MustCompile(`(?is)<img[^>]+class=["'][^"']*lazy[^"']*["'][^>]+data-src=["']([^"']*manga_covers[^"']*)["'][^>]*>`)
	chapterAnchorPattern     = regexp.MustCompile(`(?is)<a[^>]+href=["'](/reader/en/[^"']+-chapter-([0-9]+(?:-[0-9]+)?)[^"']*)["'][^>]*>(.*?)</a>`)
	chapterDatetimePattern   = regexp.MustCompile(`(?is)\bdatetime=["']([^"']+)["']`)
	chapterStatsPattern      = regexp.MustCompile(`(?is)<span[^>]*class=["'][^"']*chapter-stats[^"']*["'][^>]*>(.*?)</span>`)
	chapterTokenPattern      = regexp.MustCompile(`\d+(?:-\d+)?`)
	relativeUnitPattern      = regexp.MustCompile(`(?i)(\d+)\s*(minute|minutes|hour|hours|day|days|week|weeks|month|months|year|years)`)
	allChaptersSuffixPattern = regexp.MustCompile(`(?i)\s*\[all\s+chapters?\]\s*$`)
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
	LastUpdatedAt *time.Time
}

type chapterEntry struct {
	Chapter   float64
	URL       string
	UpdatedAt *time.Time
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     canonicalBaseURL,
		allowedHost: []string{"mgeko.cc"},
		httpClient:  connectors.NewThrottledClient(),
	}
}

func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"mgeko.cc"}
	}
	return &Connector{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
	}
}

func (c *Connector) Key() string {
	return "mgeko"
}

func (c *Connector) Name() string {
	return "Mgeko"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchPage(ctx, c.baseURL+"/browse-comics/")
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
		return nil, fmt.Errorf("url does not belong to mgeko")
	}

	slug := extractMangaSlugFromPath(parsed.Path)
	if slug == "" {
		return nil, fmt.Errorf("mgeko url must match /manga/{id}")
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

	limit = connectors.ClampSearchLimit(limit)

	body, err := c.fetchPage(ctx, c.baseURL+"/search/?search="+url.QueryEscape(query))
	if err != nil {
		return nil, fmt.Errorf("fetch mgeko search page: %w", err)
	}

	entries := parseSearchEntries(body, time.Now().UTC())
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
			URL:           c.mangaURL(entry.Slug),
			CoverImageURL: c.absoluteURL(entry.CoverImage),
		}

		if entry.LatestChapter != nil {
			latest := *entry.LatestChapter
			result.LatestChapter = &latest
		}
		if entry.LastUpdatedAt != nil {
			lastUpdatedAt := *entry.LastUpdatedAt
			result.LastUpdatedAt = &lastUpdatedAt
		}

		results = append(results, result)
		if len(results) >= limit {
			break
		}
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
		return "", fmt.Errorf("url does not belong to mgeko")
	}

	slug := extractMangaSlugFromPath(parsed.Path)
	if slug == "" {
		return "", fmt.Errorf("mgeko url must match /manga/{id}")
	}

	entries, err := c.fetchChapterEntries(ctx, slug)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		if connectors.SameChapter(entry.Chapter, chapter) {
			return entry.URL, nil
		}
	}

	return "", fmt.Errorf("chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
}

func (c *Connector) resolveBySlug(ctx context.Context, slug string) (*connectors.MangaResult, error) {
	body, err := c.fetchPage(ctx, c.baseURL+"/manga/"+url.PathEscape(slug)+"/")
	if err != nil {
		return nil, fmt.Errorf("fetch manga page: %w", err)
	}

	title := extractTitle(body, slug)
	relatedTitles := extractRelatedTitles(body, title)

	coverImageURL := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(ogImagePattern, body)))
	if coverImageURL == "" {
		coverImageURL = strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(coverDataSrcPattern, body)))
	}
	coverImageURL = c.absoluteURL(coverImageURL)

	latestChapter, lastUpdatedAt, chapterErr := c.fetchLatestChapterFromAllChapters(ctx, slug)
	if chapterErr != nil || latestChapter == nil {
		fallbackEntries := parseChapterEntries(body, time.Now().UTC())
		fallbackLatest, fallbackUpdated := selectLatestChapter(fallbackEntries)
		if latestChapter == nil {
			latestChapter = fallbackLatest
		}
		if lastUpdatedAt == nil {
			lastUpdatedAt = fallbackUpdated
		}
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  slug,
		Title:         title,
		RelatedTitles: relatedTitles,
		URL:           c.mangaURL(slug),
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: lastUpdatedAt,
	}, nil
}

func (c *Connector) fetchChapterEntries(ctx context.Context, slug string) ([]chapterEntry, error) {
	allChaptersBody, err := c.fetchPage(ctx, c.baseURL+"/manga/"+url.PathEscape(slug)+"/all-chapters/")
	if err != nil {
		return nil, fmt.Errorf("fetch all chapters page: %w", err)
	}

	entries := parseChapterEntries(allChaptersBody, time.Now().UTC())
	if len(entries) == 0 {
		return nil, fmt.Errorf("no chapter entries found")
	}

	for index := range entries {
		entries[index].URL = c.absoluteURL(entries[index].URL)
	}

	return entries, nil
}

func (c *Connector) fetchLatestChapterFromAllChapters(ctx context.Context, slug string) (*float64, *time.Time, error) {
	entries, err := c.fetchChapterEntries(ctx, slug)
	if err != nil {
		return nil, nil, err
	}

	latestChapter, latestUpdatedAt := selectLatestChapter(entries)
	if latestChapter == nil {
		return nil, nil, fmt.Errorf("no latest chapter found")
	}

	return latestChapter, latestUpdatedAt, nil
}

func parseSearchEntries(body string, now time.Time) []searchEntry {
	matches := novelItemPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	entriesBySlug := make(map[string]searchEntry, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		block := match[1]
		mangaPath := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(mangaHrefPattern, block)))
		slug := extractMangaSlugFromPath(mangaPath)
		if slug == "" {
			continue
		}

		title := connectors.CleanText(connectors.FirstSubmatch(novelTitlePattern, block))
		if title == "" {
			title = connectors.CleanText(connectors.FirstSubmatch(anchorTitleAttrPattern, block))
		}
		if title == "" {
			title = connectors.PrettifySlug(slug)
		}

		coverImageURL := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(imgDataSrcPattern, block)))
		if coverImageURL == "" {
			coverImageURL = strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(imgSrcPattern, block)))
		}
		if strings.Contains(strings.ToLower(coverImageURL), "loading.gif") {
			coverImageURL = ""
		}

		var latestChapter *float64
		if chapterRaw := strings.TrimSpace(connectors.FirstSubmatch(searchChapterPattern, block)); chapterRaw != "" {
			latestChapter = parseMgekoChapterToken(chapterRaw)
		}

		var lastUpdatedAt *time.Time
		if updatedRaw := connectors.CleanText(connectors.FirstSubmatch(searchUpdatedPattern, block)); updatedRaw != "" {
			lastUpdatedAt = parseRelativeTime(updatedRaw, now)
		}

		existing, exists := entriesBySlug[slug]
		if !exists {
			existing = searchEntry{Slug: slug}
		}
		if existing.Title == "" && title != "" {
			existing.Title = title
		}
		if existing.CoverImage == "" && coverImageURL != "" {
			existing.CoverImage = coverImageURL
		}
		if existing.LatestChapter == nil && latestChapter != nil {
			chapterValue := *latestChapter
			existing.LatestChapter = &chapterValue
		}
		if existing.LastUpdatedAt == nil && lastUpdatedAt != nil {
			updatedAtValue := *lastUpdatedAt
			existing.LastUpdatedAt = &updatedAtValue
		}
		entriesBySlug[slug] = existing
	}

	entries := make([]searchEntry, 0, len(entriesBySlug))
	for _, entry := range entriesBySlug {
		if entry.Title == "" {
			entry.Title = connectors.PrettifySlug(entry.Slug)
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Title < entries[j].Title
	})

	return entries
}

func parseChapterEntries(body string, now time.Time) []chapterEntry {
	matches := chapterAnchorPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	entries := make([]chapterEntry, 0, len(matches))
	seenByURL := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}

		chapter := parseMgekoChapterToken(strings.TrimSpace(match[2]))
		if chapter == nil {
			continue
		}

		chapterURL := strings.TrimSpace(html.UnescapeString(match[1]))
		if chapterURL == "" {
			continue
		}

		if _, exists := seenByURL[chapterURL]; exists {
			continue
		}
		seenByURL[chapterURL] = struct{}{}

		innerHTML := match[3]
		datetimeRaw := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(chapterDatetimePattern, innerHTML)))
		updatedAt := parseMgekoDatetime(datetimeRaw)
		if updatedAt == nil {
			statsRaw := connectors.CleanText(connectors.FirstSubmatch(chapterStatsPattern, innerHTML))
			updatedAt = parseRelativeTime(statsRaw, now)
		}

		entries = append(entries, chapterEntry{
			Chapter:   *chapter,
			URL:       chapterURL,
			UpdatedAt: updatedAt,
		})
	}

	return entries
}

func selectLatestChapter(entries []chapterEntry) (*float64, *time.Time) {
	if len(entries) == 0 {
		return nil, nil
	}

	var latestChapter *float64
	var latestUpdatedAt *time.Time

	for _, entry := range entries {
		if latestChapter == nil || entry.Chapter > *latestChapter {
			chapterValue := entry.Chapter
			latestChapter = &chapterValue

			if entry.UpdatedAt != nil {
				updatedAtValue := *entry.UpdatedAt
				latestUpdatedAt = &updatedAtValue
			} else {
				latestUpdatedAt = nil
			}
			continue
		}

		if latestChapter != nil && connectors.SameChapter(entry.Chapter, *latestChapter) && entry.UpdatedAt != nil {
			if latestUpdatedAt == nil || entry.UpdatedAt.After(*latestUpdatedAt) {
				updatedAtValue := *entry.UpdatedAt
				latestUpdatedAt = &updatedAtValue
			}
		}
	}

	return latestChapter, latestUpdatedAt
}

func extractTitle(body string, slug string) string {
	title := connectors.CleanText(connectors.FirstSubmatch(titleHeadingPattern, body))
	if title != "" {
		return title
	}

	title = strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaTitlePattern, body)))
	title = allChaptersSuffixPattern.ReplaceAllString(title, "")
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}

	return connectors.PrettifySlug(slug)
}

func extractRelatedTitles(body string, primaryTitle string) []string {
	candidates := make([]string, 0, 16)

	altRaw := connectors.CleanText(connectors.FirstSubmatch(altTitleHeadingPattern, body))
	if altRaw != "" {
		parts := strings.Split(altRaw, ",")
		for _, part := range parts {
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
	return connectors.FetchHTML(ctx, c.httpClient, endpoint)
}

func parseMgekoChapterToken(raw string) *float64 {
	token := chapterTokenPattern.FindString(strings.TrimSpace(raw))
	if token == "" {
		return nil
	}

	parts := strings.Split(token, "-")
	if len(parts) == 1 {
		value, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil || !connectors.ValidChapter(value) {
			return nil
		}
		return &value
	}
	if len(parts) != 2 {
		return nil
	}

	wholePart := strings.TrimSpace(parts[0])
	fractionPart := strings.TrimSpace(parts[1])
	if wholePart == "" || fractionPart == "" {
		return nil
	}

	wholeValue, err := strconv.ParseFloat(wholePart, 64)
	if err != nil {
		return nil
	}
	fractionValue, err := strconv.Atoi(fractionPart)
	if err != nil {
		return nil
	}

	value := wholeValue + float64(fractionValue)/math.Pow10(len(fractionPart))
	if !connectors.ValidChapter(value) {
		return nil
	}
	return &value
}

func parseRelativeTime(raw string, now time.Time) *time.Time {
	normalized := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(raw, "\u00a0", " ")))
	if normalized == "" {
		return nil
	}
	if strings.Contains(normalized, "just now") {
		result := now.UTC()
		return &result
	}

	matches := relativeUnitPattern.FindAllStringSubmatch(normalized, -1)
	if len(matches) == 0 {
		return nil
	}

	result := now.UTC()
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		quantity, err := strconv.Atoi(strings.TrimSpace(match[1]))
		if err != nil || quantity <= 0 {
			continue
		}

		unit := strings.TrimSpace(match[2])
		switch unit {
		case "minute", "minutes":
			result = result.Add(-time.Duration(quantity) * time.Minute)
		case "hour", "hours":
			result = result.Add(-time.Duration(quantity) * time.Hour)
		case "day", "days":
			result = result.AddDate(0, 0, -quantity)
		case "week", "weeks":
			result = result.AddDate(0, 0, -7*quantity)
		case "month", "months":
			result = result.AddDate(0, -quantity, 0)
		case "year", "years":
			result = result.AddDate(-quantity, 0, 0)
		}
	}

	return &result
}

func parseMgekoDatetime(raw string) *time.Time {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return nil
	}

	replacer := strings.NewReplacer(
		"\u00a0", " ",
		"a.m.", "AM",
		"p.m.", "PM",
		"a.m", "AM",
		"p.m", "PM",
		"A.M.", "AM",
		"P.M.", "PM",
		"A.M", "AM",
		"P.M", "PM",
		"Sept.", "Sep",
		"sept.", "Sep",
		"Jan.", "Jan",
		"jan.", "Jan",
		"Feb.", "Feb",
		"feb.", "Feb",
		"Mar.", "Mar",
		"mar.", "Mar",
		"Apr.", "Apr",
		"apr.", "Apr",
		"Jun.", "Jun",
		"jun.", "Jun",
		"Jul.", "Jul",
		"jul.", "Jul",
		"Aug.", "Aug",
		"aug.", "Aug",
		"Sep.", "Sep",
		"sep.", "Sep",
		"Oct.", "Oct",
		"oct.", "Oct",
		"Nov.", "Nov",
		"nov.", "Nov",
		"Dec.", "Dec",
		"dec.", "Dec",
	)
	normalized = replacer.Replace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")

	layouts := []string{
		"Jan 2, 2006, 3:04 PM",
		"Jan 2, 2006, 3 PM",
		"January 2, 2006, 3:04 PM",
		"January 2, 2006, 3 PM",
		"Jan 2, 2006 3:04 PM",
		"Jan 2, 2006 3 PM",
		"January 2, 2006 3:04 PM",
		"January 2, 2006 3 PM",
		"Jan 2, 2006",
		"January 2, 2006",
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

func extractMangaSlugFromPath(rawPath string) string {
	segments := strings.Split(strings.Trim(path.Clean(strings.TrimSpace(rawPath)), "/"), "/")
	if len(segments) < 2 || segments[0] != "manga" {
		return ""
	}

	slug := strings.TrimSpace(segments[1])
	if slug == "" {
		return ""
	}

	return slug
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

// ReaderRank implements connectors.SiteInfo: Mgeko is a readable aggregator in
// the default tier.
func (c *Connector) ReaderRank() int {
	return connectors.ReaderRankDefault
}

// canonicalBaseURL is the origin every returned or stored URL is built on. It
// is deliberately the production host rather than c.baseURL: c.baseURL is
// where requests go (a test server in tests), while the URLs handed back are
// stored in trackers and opened by the reader's browser, so they must always
// point at the real site.
func (c *Connector) canonicalBaseURL() string {
	return canonicalBaseURL
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

func (c *Connector) mangaURL(slug string) string {
	return c.canonicalBaseURL() + "/manga/" + slug + "/"
}
