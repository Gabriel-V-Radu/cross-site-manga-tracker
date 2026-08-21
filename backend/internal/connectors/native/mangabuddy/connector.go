package mangabuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// MangaBuddy serves a plain JSON API with no request signing and no bot
// challenge: /api/search?search= for lookups and /api/series/{slugHash} for a
// title plus its entire chapter list in one response. Series pages live at
// /series/{slugHash}, where slugHash is "{slug}.{id}".
//
// Note the host: mangabuddy.com now permanently redirects to comizy.io, which
// runs a different, incompatible backend. mangabuddy1.co.uk is a standalone site
// that kept the old branding and API, so the base URL is deliberately a variable
// rather than a hard assumption about the brand's canonical home.
const canonicalBaseURL = "https://mangabuddy1.co.uk"

// maxPlausibleChapter guards against the corrupt chapter numbers the API
// occasionally reports (one series lists a chapter 13521). Anything beyond this
// is treated as data noise rather than a real release, so it cannot inflate a
// tracker's latest-chapter count.
const maxPlausibleChapter = 10000

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     canonicalBaseURL,
		allowedHost: []string{"mangabuddy1.co.uk"},
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"mangabuddy1.co.uk"}
	}
	return &Connector{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
	}
}

func (c *Connector) Key() string {
	return "mangabuddy"
}

func (c *Connector) Name() string {
	return "MangaBuddy"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

type apiComic struct {
	ComicID  int64  `json:"comic_id"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	SlugHash string `json:"slug_hash"`
	Cover    string `json:"cover"`
	Image    string `json:"image"`
	Status   string `json:"status"`
	Kind     string `json:"kind"`
	Author   string `json:"author"`
}

type apiChapter struct {
	Number float64 `json:"number"`
	Name   string  `json:"name"`
	URL    string  `json:"url"`
}

type apiSearchResponse struct {
	Comics     []apiComic `json:"comics"`
	Pagination struct {
		CurrentPage int  `json:"current_page"`
		TotalPages  int  `json:"total_pages"`
		TotalItems  int  `json:"total_items"`
		HasNextPage bool `json:"has_next_page"`
	} `json:"pagination"`
}

type apiSeriesResponse struct {
	Comic    apiComic     `json:"comic"`
	Chapters []apiChapter `json:"chapters"`
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	var response apiSearchResponse
	return c.fetchJSON(ctx, c.baseURL+"/api/search?search=one+piece", &response)
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	slugHash, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return nil, err
	}

	series, err := c.fetchSeries(ctx, slugHash)
	if err != nil {
		return nil, fmt.Errorf("fetch mangabuddy series: %w", err)
	}

	result := c.resultFromSeries(slugHash, series)
	return &result, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	params := url.Values{}
	params.Set("search", query)

	var response apiSearchResponse
	if err := c.fetchJSON(ctx, c.baseURL+"/api/search?"+params.Encode(), &response); err != nil {
		return nil, fmt.Errorf("search mangabuddy titles: %w", err)
	}

	results := make([]connectors.MangaResult, 0, len(response.Comics))
	for _, item := range response.Comics {
		if strings.TrimSpace(item.SlugHash) == "" {
			continue
		}
		// The search payload carries no chapter list, so latest chapter is left
		// unset here; ResolveByURL fills it in for a tracked title.
		results = append(results, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  item.SlugHash,
			Title:         strings.TrimSpace(item.Title),
			RelatedTitles: buildRelatedTitles(item.Title, item.Slug),
			URL:           c.seriesURL(item.SlugHash),
			CoverImageURL: firstNonEmpty(item.Cover, item.Image),
		})
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

	slugHash, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", err
	}

	series, err := c.fetchSeries(ctx, slugHash)
	if err != nil {
		return "", fmt.Errorf("fetch mangabuddy series: %w", err)
	}

	for _, entry := range series.Chapters {
		if !sameChapterNumber(entry.Number, chapter) {
			continue
		}
		if trimmed := strings.TrimSpace(entry.URL); trimmed != "" {
			return trimmed, nil
		}
		return c.seriesURL(slugHash) + "/chapter-" + formatChapter(entry.Number), nil
	}

	return "", fmt.Errorf("chapter %s not found", formatChapter(chapter))
}

// parseSeriesURL extracts the "{slug}.{id}" identifier from a series or chapter
// URL (/series/{slugHash} and /series/{slugHash}/chapter-N).
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
		return "", fmt.Errorf("url does not belong to mangabuddy")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "series" {
		return "", fmt.Errorf("mangabuddy url must look like /series/{slug}.{id}")
	}

	identifier := strings.TrimSpace(segments[1])
	if identifier == "" {
		return "", fmt.Errorf("invalid mangabuddy series id")
	}
	return identifier, nil
}

func (c *Connector) fetchSeries(ctx context.Context, slugHash string) (*apiSeriesResponse, error) {
	var response apiSeriesResponse
	if err := c.fetchJSON(ctx, c.baseURL+"/api/series/"+slugHash, &response); err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.Comic.Title) == "" && len(response.Chapters) == 0 {
		return nil, fmt.Errorf("mangabuddy series %q not found", slugHash)
	}
	return &response, nil
}

func (c *Connector) resultFromSeries(slugHash string, series *apiSeriesResponse) connectors.MangaResult {
	title := strings.TrimSpace(series.Comic.Title)
	if title == "" {
		title = prettifySlug(series.Comic.Slug)
	}

	result := connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  slugHash,
		Title:         title,
		RelatedTitles: buildRelatedTitles(title, series.Comic.Slug),
		URL:           c.seriesURL(slugHash),
		CoverImageURL: firstNonEmpty(series.Comic.Cover, series.Comic.Image),
	}

	if latest, ok := latestChapter(series.Chapters); ok {
		result.LatestChapter = &latest
	}

	// The API's per-chapter "time" field is unreliable — it reports the newest
	// chapter of a long-running series as over a thousand days old while marking
	// chapter 0 as new — so no release timestamp is published rather than one
	// that would churn stored dates.
	return result
}

// latestChapter returns the highest plausible chapter number. The chapter array
// is ordered oldest-first, but it is scanned in full rather than read from the
// tail so that a corrupt entry anywhere cannot become the answer.
func latestChapter(chapters []apiChapter) (float64, bool) {
	latest := 0.0
	found := false
	for _, entry := range chapters {
		number := entry.Number
		if math.IsNaN(number) || math.IsInf(number, 0) {
			continue
		}
		if number <= 0 || number > maxPlausibleChapter {
			continue
		}
		if !found || number > latest {
			latest, found = number, true
		}
	}
	return latest, found
}

func (c *Connector) seriesURL(slugHash string) string {
	return c.baseURL + "/series/" + slugHash
}

func (c *Connector) fetchJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", c.baseURL+"/home")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &httpStatusError{StatusCode: res.StatusCode}
	}

	// A full series payload runs to a few hundred KB for long series, so the read
	// is capped generously rather than tightly.
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("unexpected status: %d", e.StatusCode)
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

func buildRelatedTitles(title string, slug string) []string {
	candidates := searchutil.UniqueNonEmpty([]string{prettifySlug(slug)})

	titleKey := searchutil.Normalize(title)
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateKey := searchutil.Normalize(candidate)
		if candidateKey == "" || (titleKey != "" && candidateKey == titleKey) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func prettifySlug(slug string) string {
	slug = strings.TrimSpace(strings.ReplaceAll(slug, "-", " "))
	if slug == "" {
		return ""
	}
	parts := strings.Fields(slug)
	for index := range parts {
		parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
	}
	return strings.Join(parts, " ")
}

func sameChapterNumber(a float64, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func formatChapter(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
