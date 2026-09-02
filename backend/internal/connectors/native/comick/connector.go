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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const (
	canonicalSiteURL = "https://comick.dev"
	canonicalAPIHost = "api.comick.dev"
	canonicalAPIURL  = "https://" + canonicalAPIHost
	coverCDNURL      = "https://meo.comick.pictures"
)

// site is the connector's identity. The API host is a subdomain of the primary
// domain, so listing the site domains covers it (connectors.HostAllowed matches
// subdomains). ComicK is the info floor — it always has the chapter page, which
// makes it the reliable fallback, but its reader is the worst of the chain.
var site = connectors.Site{
	SiteKey:   "comick",
	SiteName:  "ComicK",
	SiteHosts: []string{"comick.dev", "comick.io", "comick.fun"},
	Home:      canonicalSiteURL,
	Rank:      connectors.ReaderRankInfoFloor,
}

// Cloudflare 403 streaks recur site-wide (see the package comment): when one
// starts, every request is doomed until it lifts, so the connector keeps an
// escalating breaker with the same relapse semantics as MangaFire's. The base
// is shorter than MangaFire's challenge cooldown because ComicK's streaks have
// been temporary IP-level blocks that lift within minutes to hours, not
// site-wide configuration changes.
const (
	cooldownBase          = 10 * time.Minute
	cooldownRelapseWindow = 90 * time.Minute
	maxCooldown           = 6 * time.Hour
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

// Connector reads ComicK through its JSON API. The embedded Site supplies Key,
// Name, Kind, the SiteInfo methods and the URL helpers; its Home is the reader
// origin returned/stored URLs are built on, which is distinct from apiURL, the
// host requests go to (the live API, or a test server).
type Connector struct {
	connectors.Site
	apiURL     string
	httpClient *http.Client
	breaker    *connectors.EscalatingBreaker

	mu     sync.Mutex
	comics map[string]comicRecord
}

func NewConnector() *Connector {
	return &Connector{
		Site:       site,
		apiURL:     canonicalAPIURL,
		httpClient: connectors.NewThrottledClient(),
		breaker:    connectors.NewEscalatingBreaker(cooldownRelapseWindow, maxCooldown),
		comics:     map[string]comicRecord{},
	}
}

// NewConnectorWithOptions points the connector at another API host (a test
// server), optionally building stored URLs on another site origin and claiming
// other hosts. A nil client gets the shared throttled one, so no caller can
// construct an unpaced connector by accident; tests that want to stay unpaced
// pass their own.
func NewConnectorWithOptions(siteURL string, apiURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = connectors.NewThrottledClient()
	}
	identity := site
	if home := strings.TrimRight(strings.TrimSpace(siteURL), "/"); home != "" {
		identity.Home = home
	}
	if len(allowedHost) > 0 {
		identity.SiteHosts = allowedHost
	}
	return &Connector{
		Site:       identity,
		apiURL:     strings.TrimRight(strings.TrimSpace(apiURL), "/"),
		httpClient: client,
		breaker:    connectors.NewEscalatingBreaker(cooldownRelapseWindow, maxCooldown),
		comics:     map[string]comicRecord{},
	}
}

// CooldownRemaining implements connectors.CooldownReporter, so the poller
// skips ComicK outright while a Cloudflare 403 streak has the breaker open.
func (c *Connector) CooldownRemaining() (time.Duration, string) {
	return c.breaker.Remaining()
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
	if connectors.IsNotFound(err) {
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

	endpoint := c.apiURL + "/comic/" + url.PathEscape(slug) + "?tachiyomi=true"
	var comicResponse apiComicResponse
	if err := c.fetchJSON(ctx, endpoint, &comicResponse); err != nil {
		return comicRecord{}, fmt.Errorf("fetch comick comic: %w", err)
	}
	comic := comicResponse.Comic
	if strings.TrimSpace(comic.HID) == "" {
		// A record without a hid is the API's way of saying the slug names no
		// comic; typed as a 404 so callers classify it with IsNotFound.
		return comicRecord{}, fmt.Errorf("comick comic %q not found: %w", slug, &connectors.HTTPStatusError{StatusCode: http.StatusNotFound, URL: endpoint})
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

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("q", query.Raw)
	params.Set("limit", strconv.Itoa(query.Limit))

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
		if !query.Matches(append([]string{record.title, slug}, record.relatedTitles...)...) {
			continue
		}
		// The search payload's chapter count spans every language, so latest
		// chapter is left unset; ResolveByURL fills in the English number.
		mapped = append(mapped, c.resultFromRecord(slug, record))
		if len(mapped) >= query.Limit {
			break
		}
	}

	return mapped, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapter(chapter) {
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

	formatted := connectors.FormatChapter(chapter)
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
		return c.HomeURL() + "/comic/" + url.PathEscape(slug) + "/" + entry.HID + "-chapter-" + formatted + "-en", nil
	}

	return "", fmt.Errorf("chapter %s: %w", formatted, connectors.ErrChapterNotFound)
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

	var latest connectors.LatestReading
	for _, entry := range response.Chapters {
		number, err := strconv.ParseFloat(strings.TrimSpace(entry.Chap), 64)
		if err != nil || !connectors.ValidChapter(number) {
			continue
		}
		latest.Add(number, parseChapterTime(entry.PublishAt, entry.CreatedAt))
	}
	best, bestAt := latest.Result()
	return best, bestAt, nil
}

func parseChapterTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed := connectors.ParseFirstTime(value, time.RFC3339); parsed != nil {
			return parsed
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

	record := comicRecord{
		hid:           strings.TrimSpace(comic.HID),
		title:         title,
		relatedTitles: searchutil.RelatedTitles(title, related),
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
		URL:           c.HomeURL() + "/comic/" + url.PathEscape(slug),
		CoverImageURL: record.coverURL,
	}
}

// parseSeriesURL extracts the series slug from a comic or chapter URL
// (/comic/{slug} and /comic/{slug}/{chapterHid}-chapter-N-en).
func (c *Connector) parseSeriesURL(rawURL string) (string, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", err
	}

	segments := connectors.PathSegments(parsed)
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
	if remaining, reason := c.breaker.Remaining(); remaining > 0 {
		// The same error the shared throttle returns for an open circuit, so
		// callers classify both breakers identically.
		return fmt.Errorf("comick %s: %w", reason, &connectors.SourceCoolingDownError{Host: canonicalAPIHost, RetryAfter: remaining})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", canonicalSiteURL+"/")

	if err := connectors.DoJSON(c.httpClient, req, target, 0); err != nil {
		// A 403 here is a Cloudflare streak, not a per-request verdict (the
		// package comment documents the recurring pattern): open the breaker
		// so the rest of the streak fails fast, escalating on relapse.
		if connectors.IsHTTPStatus(err, http.StatusForbidden) {
			c.breaker.Trip(cooldownBase, "Cloudflare refused us (403 streak)")
		}
		return err
	}
	c.breaker.NoteSuccess()
	return nil
}
