// Package comick reads ComicK (comick.dev, API at api.comick.dev), a
// MangaDex-style community database with aggregator-class coverage: official
// rips (MangaPlus etc.) land alongside scanlations, so it does not freeze on
// licensed series, and chapters are queryable per language, so English
// numbers never inflate with raw-Japanese counts. Measured 2026-08-21 it was
// the only source exact on all seven benchmark series, several uploaded the
// same day.
//
// The API is open JSON behind Cloudflare with no signing, but it is
// burst-sensitive: a dozen rapid requests earn a temporary 403 streak, which
// is why the shared throttle carries a widened gap for its host. Go's default
// TLS fingerprint passes (verified live, unlike freewebnovel's host).
//
// Endpoints used:
//   - GET /v1.0/search?q=&limit=          → hid, slug, title, md_titles, md_covers
//   - GET /comic/{slug}?tachiyomi=true    → the comic record for a series URL
//   - GET /comic/{hid}/chapters?lang=en&chap=&limit= → English chapters,
//     newest first, with real publish timestamps and the chapter hid the
//     reader URL is built from: /comic/{slug}/{chapterHid}-chapter-{n}-en.
package comick

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const (
	canonicalSiteURL = "https://comick.dev"
	canonicalAPIURL  = "https://api.comick.dev"
	coverCDNURL      = "https://meo.comick.pictures"
)

// chapterProbeLimit is how many newest chapters a resolve reads: the top entry
// can be a numberless special (chap is null on oneshots), so a small window is
// scanned for the highest real number instead of trusting chapters[0].
const chapterProbeLimit = 10

// comicCacheTTL bounds how long a slug's comic record (hid, title, cover,
// alternate titles) is reused before being refetched. The record is stable —
// the hid never changes for a slug — so the TTL only exists to pick up title
// or cover edits eventually. Caching it turns the steady-state resolve into a
// single chapters request, which matters twice over: the API's host gap is a
// wide 3.5s, and a dashboard cover pass fires dozens of resolves at once.
const comicCacheTTL = 24 * time.Hour

// comicRecord is the cached comic-payload half of a resolve.
type comicRecord struct {
	hid           string
	title         string
	coverURL      string
	relatedTitles []string
	fetchedAt     time.Time
}

type Connector struct {
	siteURL     string
	apiURL      string
	allowedHost []string
	httpClient  *http.Client

	mu     sync.Mutex
	comics map[string]comicRecord
}

func NewConnector() *Connector {
	return &Connector{
		siteURL:     canonicalSiteURL,
		apiURL:      canonicalAPIURL,
		allowedHost: []string{"comick.dev", "comick.io", "comick.fun"},
		httpClient:  connectors.NewThrottledClient(20 * time.Second),
		comics:      map[string]comicRecord{},
	}
}

func NewConnectorWithOptions(siteURL string, apiURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"comick.dev", "comick.io", "comick.fun"}
	}
	return &Connector{
		siteURL:     strings.TrimRight(strings.TrimSpace(siteURL), "/"),
		apiURL:      strings.TrimRight(strings.TrimSpace(apiURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
		comics:      map[string]comicRecord{},
	}
}

func (c *Connector) Key() string {
	return "comick"
}

func (c *Connector) Name() string {
	return "ComicK"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

type apiTitle struct {
	Title string `json:"title"`
}

type apiCover struct {
	B2Key string `json:"b2key"`
}

type apiComic struct {
	HID      string     `json:"hid"`
	Slug     string     `json:"slug"`
	Title    string     `json:"title"`
	MDTitles []apiTitle `json:"md_titles"`
	MDCovers []apiCover `json:"md_covers"`
}

type apiComicResponse struct {
	Comic apiComic `json:"comic"`
}

type apiChapter struct {
	HID       string `json:"hid"`
	Chap      string `json:"chap"`
	Lang      string `json:"lang"`
	PublishAt string `json:"publish_at"`
	CreatedAt string `json:"created_at"`
}

type apiChaptersResponse struct {
	Chapters []apiChapter `json:"chapters"`
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	var results []apiComic
	return c.fetchJSON(ctx, c.apiURL+"/v1.0/search?q=one+piece&limit=1", &results)
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	slug, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return nil, err
	}

	record, err := c.comicRecordFor(ctx, slug)
	if err != nil {
		return nil, err
	}

	// English-scoped on purpose: the series-level last_chapter counts every
	// language, which is exactly the inflation this app avoids.
	latest, publishedAt, err := c.latestEnglishChapter(ctx, record.hid)
	if isNotFound(err) {
		// A stale cached hid (the comic moved or was merged) answers 404 here;
		// refetching the record self-heals it.
		c.forgetComic(slug)
		if record, err = c.comicRecordFor(ctx, slug); err != nil {
			return nil, err
		}
		latest, publishedAt, err = c.latestEnglishChapter(ctx, record.hid)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch comick chapters: %w", err)
	}

	result := c.resultFromRecord(slug, record)
	if latest != nil {
		result.LatestChapter = latest
		result.LastUpdatedAt = publishedAt
	}

	return &result, nil
}

// comicRecordFor returns the slug's comic record, fetching and caching it when
// missing or expired. The cache is what keeps a steady-state resolve at one
// API request instead of two.
func (c *Connector) comicRecordFor(ctx context.Context, slug string) (comicRecord, error) {
	c.mu.Lock()
	record, ok := c.comics[slug]
	c.mu.Unlock()
	if ok && time.Since(record.fetchedAt) < comicCacheTTL {
		return record, nil
	}

	var comicResponse apiComicResponse
	if err := c.fetchJSON(ctx, c.apiURL+"/comic/"+url.PathEscape(slug)+"?tachiyomi=true", &comicResponse); err != nil {
		return comicRecord{}, fmt.Errorf("fetch comick comic: %w", err)
	}
	comic := comicResponse.Comic
	if strings.TrimSpace(comic.HID) == "" {
		return comicRecord{}, fmt.Errorf("comick comic %q not found", slug)
	}

	record = recordFromComic(comic)
	c.storeComic(slug, record)
	return record, nil
}

func (c *Connector) storeComic(slug string, record comicRecord) {
	c.mu.Lock()
	c.comics[slug] = record
	c.mu.Unlock()
}

func (c *Connector) forgetComic(slug string) {
	c.mu.Lock()
	delete(c.comics, slug)
	c.mu.Unlock()
}

func isNotFound(err error) bool {
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == 404
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
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(limit))

	var results []apiComic
	if err := c.fetchJSON(ctx, c.apiURL+"/v1.0/search?"+params.Encode(), &results); err != nil {
		return nil, fmt.Errorf("search comick titles: %w", err)
	}

	mapped := make([]connectors.MangaResult, 0, len(results))
	for _, comic := range results {
		slug := strings.TrimSpace(comic.Slug)
		if slug == "" || strings.TrimSpace(comic.HID) == "" {
			continue
		}
		// The search payload carries the whole comic record, so it seeds the
		// cache: a resolve right after a search costs one request.
		record := recordFromComic(comic)
		c.storeComic(slug, record)
		// The search payload's chapter count spans every language, so latest
		// chapter is left unset; ResolveByURL fills in the English number.
		mapped = append(mapped, c.resultFromRecord(slug, record))
		if len(mapped) >= limit {
			break
		}
	}

	return mapped, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if math.IsNaN(chapter) || math.IsInf(chapter, 0) || chapter <= 0 {
		return "", fmt.Errorf("invalid chapter")
	}

	slug, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", err
	}

	record, err := c.comicRecordFor(ctx, slug)
	if err != nil {
		return "", err
	}

	formatted := formatChapter(chapter)
	params := url.Values{}
	params.Set("lang", "en")
	params.Set("chap", formatted)
	params.Set("limit", "1")

	var chaptersResponse apiChaptersResponse
	if err := c.fetchJSON(ctx, c.apiURL+"/comic/"+url.PathEscape(record.hid)+"/chapters?"+params.Encode(), &chaptersResponse); err != nil {
		return "", fmt.Errorf("fetch comick chapters: %w", err)
	}
	for _, entry := range chaptersResponse.Chapters {
		if strings.TrimSpace(entry.HID) == "" {
			continue
		}
		return c.siteURL + "/comic/" + url.PathEscape(slug) + "/" + entry.HID + "-chapter-" + formatted + "-en", nil
	}

	return "", fmt.Errorf("chapter %s not found", formatted)
}

// latestEnglishChapter reads the newest English chapters and returns the
// highest parseable number with its publish timestamp.
func (c *Connector) latestEnglishChapter(ctx context.Context, hid string) (*float64, *time.Time, error) {
	params := url.Values{}
	params.Set("lang", "en")
	params.Set("limit", strconv.Itoa(chapterProbeLimit))

	var response apiChaptersResponse
	if err := c.fetchJSON(ctx, c.apiURL+"/comic/"+url.PathEscape(hid)+"/chapters?"+params.Encode(), &response); err != nil {
		return nil, nil, err
	}

	var (
		best   *float64
		bestAt *time.Time
	)
	for _, entry := range response.Chapters {
		number, err := strconv.ParseFloat(strings.TrimSpace(entry.Chap), 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
			continue
		}
		if best != nil && number <= *best {
			continue
		}
		value := number
		best = &value
		bestAt = parseChapterTime(entry.PublishAt, entry.CreatedAt)
	}
	return best, bestAt, nil
}

func parseChapterTime(values ...string) *time.Time {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func recordFromComic(comic apiComic) comicRecord {
	title := strings.TrimSpace(comic.Title)

	related := make([]string, 0, len(comic.MDTitles))
	for _, entry := range comic.MDTitles {
		related = append(related, entry.Title)
	}
	titleKey := searchutil.Normalize(title)
	filtered := make([]string, 0, len(related))
	for _, candidate := range searchutil.UniqueNonEmpty(related) {
		if searchutil.Normalize(candidate) == titleKey {
			continue
		}
		filtered = append(filtered, candidate)
	}

	record := comicRecord{
		hid:           strings.TrimSpace(comic.HID),
		title:         title,
		relatedTitles: filtered,
		fetchedAt:     time.Now(),
	}
	for _, cover := range comic.MDCovers {
		if key := strings.TrimSpace(cover.B2Key); key != "" {
			record.coverURL = coverCDNURL + "/" + key
			break
		}
	}
	return record
}

func (c *Connector) resultFromRecord(slug string, record comicRecord) connectors.MangaResult {
	return connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  record.hid,
		Title:         record.title,
		RelatedTitles: record.relatedTitles,
		URL:           c.siteURL + "/comic/" + url.PathEscape(slug),
		CoverImageURL: record.coverURL,
	}
}

// parseSeriesURL extracts the series slug from a comic or chapter URL
// (/comic/{slug} and /comic/{slug}/{chapterHid}-chapter-N-en).
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
		return "", fmt.Errorf("url does not belong to comick")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "comic" {
		return "", fmt.Errorf("comick url must look like /comic/{slug}")
	}

	slug := strings.TrimSpace(segments[1])
	if slug == "" {
		return "", fmt.Errorf("invalid comick series slug")
	}
	return slug, nil
}

func (c *Connector) fetchJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", canonicalSiteURL+"/")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &httpStatusError{StatusCode: res.StatusCode}
	}

	// The comic payload carries reviews and recommendations and runs to some
	// tens of KB; the cap is a generous safety bound, not a size estimate.
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

func formatChapter(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
