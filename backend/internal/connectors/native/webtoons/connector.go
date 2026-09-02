package webtoons

import (
	"context"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

const (
	canonicalBaseURL = "https://www.webtoons.com"
	// imageCDNURL is where the search API's thumbnail paths resolve: that
	// payload spells them relative to Naver's image host, not to the site.
	imageCDNURL = "https://swebtoon-phinf.pstatic.net"
	// searchLocale scopes the immediate-search endpoint to the English site,
	// which is the only catalogue whose episode numbers this app tracks.
	searchLocale = "en"
)

// site is the connector's identity. The regional, mobile and www sites are all
// subdomains of webtoons.com, which connectors.HostAllowed covers; the
// thumbnail CDN is Naver's shared image host and is deliberately not claimed.
// WEBTOON is a readable site in the default tier.
var site = connectors.Site{
	SiteKey:   "webtoons",
	SiteName:  "WEBTOON",
	SiteHosts: []string{"webtoons.com"},
	Home:      canonicalBaseURL,
	Rank:      connectors.ReaderRankDefault,
}

var (
	canonicalPattern    = regexp.MustCompile(`(?is)<link\s+[^>]*rel=["']canonical["'][^>]*href=["']([^"']+)["']`)
	metaTitlePattern    = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	metaImagePattern    = regexp.MustCompile(`(?is)<meta\s+[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`)
	titleHeadingPattern = regexp.MustCompile(`(?is)<h1[^>]*class=["'][^"']*subj[^"']*["'][^>]*>(.*?)</h1>`)
	episodeItemPattern  = regexp.MustCompile(`(?is)<li[^>]*class=["'][^"']*_episodeItem[^"']*["'][^>]*data-episode-no=["'](\d+)["'][^>]*>(.*?)</li>`)
	episodeHrefPattern  = regexp.MustCompile(`(?is)href=["']([^"']*episode_no=\d+[^"']*)["']`)
	episodeDatePattern  = regexp.MustCompile(`(?is)<span[^>]*class=["'][^"']*date[^"']*["'][^>]*>([^<]+)</span>`)
)

// Connector reads webtoons.com through its immediate-search API and its
// episode-list pages. The embedded Site supplies Key, Name, Kind, the SiteInfo
// methods and the URL helpers; baseURL is where requests go (the live site, or
// a test server).
type Connector struct {
	connectors.Site
	baseURL    string
	httpClient *http.Client
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
	_, err := c.searchImmediate(ctx, "webtoon")
	if err != nil {
		return err
	}
	return nil
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	titleNo, err := c.parseTitleURL(rawURL)
	if err != nil {
		return nil, err
	}
	return c.resolveByTitleNo(ctx, titleNo)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	payload, err := c.searchImmediate(ctx, query.Raw)
	if err != nil {
		return nil, err
	}

	seen := make(map[int]struct{}, len(payload.Result.SearchedList))
	results := make([]connectors.MangaResult, 0, min(query.Limit, len(payload.Result.SearchedList)))
	for _, item := range payload.Result.SearchedList {
		if !strings.EqualFold(strings.TrimSpace(item.SearchMode), "TITLE") {
			continue
		}
		if !query.Matches(item.Title) {
			continue
		}
		if item.TitleNo <= 0 {
			continue
		}
		if _, ok := seen[item.TitleNo]; ok {
			continue
		}

		result := connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  strconv.Itoa(item.TitleNo),
			Title:         strings.TrimSpace(item.Title),
			URL:           c.canonicalEpisodeListURL(item.TitleNo),
			CoverImageURL: absoluteImageURL(item.ThumbnailMobile),
		}

		// Enrich with latest episode/date to improve tracker auto-fill
		// reliability. The search payload carries neither, so each hit costs a
		// series page; the timeout bounds one slow page instead of letting it
		// hold up the whole result set.
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

		if len(results) >= query.Limit {
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

	titleNo, err := c.parseTitleURL(rawURL)
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
		return "", fmt.Errorf("episode %d not found: %w", episodeNo, connectors.ErrChapterNotFound)
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

	return "", fmt.Errorf("episode %d not found: %w", episodeNo, connectors.ErrChapterNotFound)
}

// parseTitleURL checks the URL is this site's and reads the title number out
// of its query string.
func (c *Connector) parseTitleURL(rawURL string) (int, error) {
	parsedURL, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return 0, err
	}
	return extractTitleNo(parsedURL)
}

func (c *Connector) searchImmediate(ctx context.Context, query string) (*immediateSearchResponse, error) {
	endpoint := c.baseURL + "/" + searchLocale + "/search/immediate?keyword=" + url.QueryEscape(strings.TrimSpace(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	// Only the Accept the site's own search XHR sends is set here; the browser
	// User-Agent and Accept-Language this endpoint also expects come from the
	// shared fetch helper.
	req.Header.Set("Accept", "application/json, text/plain, */*")

	var payload immediateSearchResponse
	if err := connectors.DoJSON(c.httpClient, req, &payload, 0); err != nil {
		return nil, fmt.Errorf("search webtoons titles: %w", err)
	}
	if !payload.Success {
		return nil, fmt.Errorf("webtoons search was not successful")
	}

	return &payload, nil
}

func (c *Connector) resolveByTitleNo(ctx context.Context, titleNo int) (*connectors.MangaResult, error) {
	body, finalURL, err := c.fetchPage(ctx, c.episodeListURL(titleNo, 1))
	if err != nil {
		return nil, err
	}

	// The page states its own canonical URL (the localized, slugged one a
	// reader recognizes), so it wins over the URL the request happened to end
	// on, which in turn beats the bare episodeList form.
	canonicalURL := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(canonicalPattern, body)))
	if canonicalURL == "" {
		canonicalURL = strings.TrimSpace(finalURL)
	}
	if canonicalURL == "" {
		canonicalURL = c.canonicalEpisodeListURL(titleNo)
	}

	title := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaTitlePattern, body)))
	if title == "" {
		title = strings.TrimSpace(html.UnescapeString(connectors.CleanText(connectors.FirstSubmatch(titleHeadingPattern, body))))
	}
	if title == "" {
		title = "WEBTOON " + strconv.Itoa(titleNo)
	}

	coverImageURL := c.AbsoluteURL(strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(metaImagePattern, body))))

	// Episode numbers were validated in extractEpisodeEntries.
	var latest connectors.LatestReading
	for _, entry := range c.extractEpisodeEntries(body) {
		latest.Add(float64(entry.Number), parseWebtoonsDate(entry.DateRaw))
	}
	latestChapter, latestUpdatedAt := latest.Result()

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
	body, _, err := c.fetchPage(ctx, c.episodeListURL(titleNo, page))
	if err != nil {
		return nil, err
	}
	return c.extractEpisodeEntries(body), nil
}

// episodeListPath is the series page this connector asks for and, when a page
// carries no canonical link of its own, hands back.
func episodeListPath(titleNo int, page int) string {
	values := url.Values{}
	values.Set("titleNo", strconv.Itoa(titleNo))
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	return "/episodeList?" + values.Encode()
}

func (c *Connector) episodeListURL(titleNo int, page int) string {
	return c.baseURL + episodeListPath(titleNo, page)
}

// canonicalEpisodeListURL is the stored form of a series page: only page one,
// because a stored link is where the reader lands, not where a scrape paged to.
func (c *Connector) canonicalEpisodeListURL(titleNo int) string {
	return c.HomeURL() + episodeListPath(titleNo, 1)
}

// fetchPage returns the page body together with the URL the request finished
// on. It cannot use connectors.FetchHTML, which drops that final URL, because
// a resolve falls back to it when the page states no canonical link.
func (c *Connector) fetchPage(ctx context.Context, endpoint string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	body, finalURL, err := connectors.FetchBytes(c.httpClient, req, 0)
	if err != nil {
		return "", "", err
	}

	return string(body), finalURL, nil
}

// absoluteImageURL resolves a search thumbnail, which the API spells relative
// to the image CDN rather than to the site.
func absoluteImageURL(raw string) string {
	return connectors.AbsoluteURL(imageCDNURL, raw)
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
	if !connectors.ValidChapter(chapter) {
		return 0, fmt.Errorf("invalid chapter")
	}

	// WEBTOON numbers episodes, never half-chapters, so a fractional tracker
	// value cannot address anything on the site.
	rounded := math.Round(chapter)
	if !connectors.SameChapter(chapter, rounded) {
		return 0, fmt.Errorf("webtoons chapter must be a whole episode number")
	}

	episode := int(rounded)
	if episode <= 0 {
		return 0, fmt.Errorf("invalid chapter")
	}
	return episode, nil
}

func (c *Connector) extractEpisodeEntries(body string) []episodeEntry {
	matches := episodeItemPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	entries := make([]episodeEntry, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		// The episode number drives the tracker, so a row whose attribute is
		// not a plausible episode (a year, an id) is dropped rather than
		// inflating the latest-episode figure.
		number, err := strconv.Atoi(strings.TrimSpace(match[1]))
		if err != nil || !connectors.ValidChapter(float64(number)) {
			continue
		}

		block := match[2]
		href := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(episodeHrefPattern, block)))
		dateRaw := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(episodeDatePattern, block)))

		entry := episodeEntry{
			Number:  number,
			URL:     c.AbsoluteURL(href),
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

// parseWebtoonsDate reads an episode row's date. Only the English site's
// formats are listed: a locale spelled otherwise leaves the release undated
// rather than being guessed into the wrong day.
func parseWebtoonsDate(raw string) *time.Time {
	return connectors.ParseFirstTime(raw,
		"Jan 2, 2006",
		"January 2, 2006",
		"2006-01-02",
	)
}
