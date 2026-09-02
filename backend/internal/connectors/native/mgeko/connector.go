package mgeko

import (
	"context"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const canonicalBaseURL = "https://www.mgeko.cc"

// nonBreakingSpace (U+00A0) is what the site pads its timestamps with; it is
// folded to a plain space before any date text is parsed.
var nonBreakingSpace = string(rune(0x00A0))

// site is the connector's identity. Mgeko is a readable aggregator in the
// default tier.
var site = connectors.Site{
	SiteKey:   "mgeko",
	SiteName:  "Mgeko",
	SiteHosts: []string{"mgeko.cc"},
	Home:      canonicalBaseURL,
	Rank:      connectors.ReaderRankDefault,
}

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

// Connector reads mgeko.cc by scraping its search, manga and all-chapters
// pages. The embedded Site supplies Key, Name, Kind, the SiteInfo methods and
// the URL helpers; baseURL is where requests go (the live site, or a test
// server).
type Connector struct {
	connectors.Site
	baseURL    string
	httpClient *http.Client
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
	_, err := c.fetchPage(ctx, c.baseURL+"/browse-comics/")
	return err
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	slug, err := c.parseMangaURL(rawURL)
	if err != nil {
		return nil, err
	}
	return c.resolveBySlug(ctx, slug)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	body, err := c.fetchPage(ctx, c.baseURL+"/search/?search="+url.QueryEscape(query.Raw))
	if err != nil {
		return nil, fmt.Errorf("fetch mgeko search page: %w", err)
	}

	entries := parseSearchEntries(body, time.Now().UTC())
	if len(entries) == 0 {
		return []connectors.MangaResult{}, nil
	}

	results := make([]connectors.MangaResult, 0, min(query.Limit, len(entries)))
	for _, entry := range entries {
		if !query.Matches(entry.Title, entry.Slug) {
			continue
		}

		result := connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  entry.Slug,
			Title:         entry.Title,
			URL:           c.mangaURL(entry.Slug),
			CoverImageURL: c.AbsoluteURL(entry.CoverImage),
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
		if len(results) >= query.Limit {
			break
		}
	}

	return results, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapter(chapter) {
		return "", fmt.Errorf("invalid chapter")
	}

	slug, err := c.parseMangaURL(rawURL)
	if err != nil {
		return "", err
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

// parseMangaURL checks the URL is this site's and reads the slug out of its
// /manga/{id} path.
func (c *Connector) parseMangaURL(rawURL string) (string, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", err
	}

	slug := mangaSlugFromSegments(connectors.PathSegments(parsed))
	if slug == "" {
		return "", fmt.Errorf("mgeko url must match /manga/{id}")
	}
	return slug, nil
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
	coverImageURL = c.AbsoluteURL(coverImageURL)

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
		entries[index].URL = c.AbsoluteURL(entries[index].URL)
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

// selectLatestChapter folds the chapter list (already validated by
// parseMgekoChapterToken) into the highest chapter and its date.
func selectLatestChapter(entries []chapterEntry) (*float64, *time.Time) {
	var latest connectors.LatestReading
	for _, entry := range entries {
		latest.Add(entry.Chapter, entry.UpdatedAt)
	}
	return latest.Result()
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

// extractRelatedTitles gathers the alternate names the manga page lists — the
// comma-separated alternative-title heading first, then whatever the shared
// extractor finds — and keeps the ones worth storing beside the primary title.
func extractRelatedTitles(body string, primaryTitle string) []string {
	candidates := make([]string, 0, 16)

	altRaw := connectors.CleanText(connectors.FirstSubmatch(altTitleHeadingPattern, body))
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
	return searchutil.RelatedTitles(primaryTitle, candidates)
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
	normalized := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(raw, " ", " ")))
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
		" ", " ",
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

// extractMangaSlugFromPath reads the slug out of a /manga/{id}/ href as it
// appears in a search result block.
func extractMangaSlugFromPath(rawPath string) string {
	return mangaSlugFromSegments(connectors.PathSegments(&url.URL{Path: strings.TrimSpace(rawPath)}))
}

func mangaSlugFromSegments(segments []string) string {
	if len(segments) < 2 || segments[0] != "manga" {
		return ""
	}
	return strings.TrimSpace(segments[1])
}

// mangaURL is the stored, canonical address of a series: always on the real
// site, never on the base URL requests go to.
func (c *Connector) mangaURL(slug string) string {
	return c.HomeURL() + "/manga/" + slug + "/"
}
