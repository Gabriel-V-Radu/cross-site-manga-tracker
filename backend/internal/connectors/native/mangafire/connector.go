package mangafire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// MangaFire rebuilt their site as a SPA backed by a JSON API under /api.
// Manga pages moved from /manga/{slug}.{hid} to /title/{hid}-{slug} and reader
// pages from /read/{slug}.{hid}/{lang}/chapter-{n} to
// /title/{hid}-{slug}/chapter/{chapterId}, which is the form the site's own
// chapter lists link to. Both path segments are cosmetic: the router keys on
// the hid and the chapter id, so /title/{hid}/chapter/{chapterId} opens the
// same page.
//
// The legacy /read/... URLs still resolve, but only as a redirect to the series
// page — the chapter part is dropped (verified 2026-09-01 on several titles).
// That is why this connector deliberately does not implement
// connectors.OfflineChapterLinker; see ResolveChapterURL.

const (
	canonicalHost    = "mangafire.to"
	canonicalBaseURL = "https://" + canonicalHost
)

// site is the connector's identity. Its Home is the origin every returned or
// stored URL is built on: deliberately the production host rather than
// baseURL, which is where requests go (a test server in tests), while the URLs
// handed back are stored in trackers and opened by the reader's browser, so
// they must always point at the real site. MangaFire is a readable aggregator
// in the default tier.
var site = connectors.Site{
	SiteKey:   "mangafire",
	SiteName:  "MangaFire",
	SiteHosts: []string{canonicalHost},
	Home:      canonicalBaseURL,
	Rank:      connectors.ReaderRankDefault,
}

// chapterLanguage is the only language this connector reads. MangaFire hosts a
// title in several languages at once and its title payload describes all of them
// together: `latestChapter` is the highest number in *any* language, and
// `chapterUpdatedAt` is when that chapter was uploaded. For a series whose
// Japanese raws run ahead of the English scanlation both fields describe a
// release the English reader cannot read — Reiwa no Dara-san reported chapter
// 57.5 from a Japanese batch six months old while English stood at 54 and had
// updated that day — and neither field moves when a new English chapter lands,
// so such a title looks permanently stalled. The chapter listing is the only
// endpoint that can be scoped to a language, so every chapter number and release
// date this connector reports is derived from it.
const chapterLanguage = "en"

// Connector reads MangaFire through its signed JSON API. The embedded Site
// supplies Key, Name, Kind, the SiteInfo methods and the URL helpers; baseURL
// is where requests go (the live site, or a test server).
type Connector struct {
	connectors.Site
	baseURL    string
	httpClient *http.Client
	signer     *signer

	// breaker classifies outages the shared per-host throttle cannot see
	// (Cloudflare challenges, token rejections) and escalates its cooldown on
	// every relapse. Request pacing lives in the shared throttle: mangafire.to
	// carries a widened host gap there, so the connector no longer paces on
	// top of it.
	breaker *connectors.EscalatingBreaker
}

func NewConnector() *Connector {
	return &Connector{
		Site:       site,
		baseURL:    canonicalBaseURL,
		httpClient: connectors.NewThrottledClient(),
		signer:     newSigner(),
		breaker:    connectors.NewEscalatingBreaker(cooldownRelapseWindow, maxCooldown),
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
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: client,
		signer:     newSigner(),
		breaker:    connectors.NewEscalatingBreaker(cooldownRelapseWindow, maxCooldown),
	}
}

type apiPoster struct {
	Small  string `json:"small"`
	Medium string `json:"medium"`
	Large  string `json:"large"`
}

type apiTitle struct {
	HID       string     `json:"hid"`
	Slug      string     `json:"slug"`
	Title     string     `json:"title"`
	Poster    *apiPoster `json:"poster"`
	AltTitles []string   `json:"altTitles"`

	// Mapped but deliberately never read: both describe every language at once
	// (see chapterLanguage), so reporting them would hand the reader a chapter
	// number and a date from a translation they do not follow. The English
	// chapter listing is the source for both.
	LatestChapter    *float64 `json:"latestChapter"`
	ChapterUpdatedAt string   `json:"chapterUpdatedAt"`
}

type apiTitlesResponse struct {
	Items []apiTitle `json:"items"`
}

type apiTitleDetailResponse struct {
	Data apiTitle `json:"data"`
}

type apiChapter struct {
	ID        int64   `json:"id"`
	Number    float64 `json:"number"`
	Language  string  `json:"language"`
	CreatedAt int64   `json:"createdAt"`
}

type apiMeta struct {
	Page     int  `json:"page"`
	PerPage  int  `json:"perPage"`
	LastPage int  `json:"lastPage"`
	Total    int  `json:"total"`
	HasNext  bool `json:"hasNext"`
}

type apiChaptersResponse struct {
	Items []apiChapter `json:"items"`
	Meta  apiMeta      `json:"meta"`
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	params := url.Values{}
	params.Set("limit", "1")
	var response apiTitlesResponse
	if err := c.fetchAPI(ctx, "/api/titles", params, &response); err != nil {
		return err
	}
	return nil
}

// fetchAPI signs an API request and fetches it. path is the request path only
// (e.g. "/api/titles/dkw"); params are the query params sent alongside the
// mandatory `vrf` token. MangaFire returns 403 {"message":"Missing token."}
// for any /api/titles* request without a valid vrf, so every such call routes
// through here. The params signed are exactly the params sent (minus vrf), which
// is what the server re-validates against.
func (c *Connector) fetchAPI(ctx context.Context, path string, params url.Values, target any) error {
	if params == nil {
		params = url.Values{}
	}

	token, err := c.signer.Sign(path, params)
	if err != nil {
		return fmt.Errorf("sign mangafire request: %w", err)
	}

	query := url.Values{}
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	query.Set("vrf", token)

	return c.fetchJSON(ctx, c.baseURL+path+"?"+query.Encode(), target)
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	hid, _, err := c.parseTitleURL(rawURL)
	if err != nil {
		return nil, err
	}

	detail, err := c.fetchTitleDetail(ctx, hid)
	if err != nil {
		return nil, fmt.Errorf("fetch manga detail: %w", err)
	}

	result := c.resultFromAPITitle(*detail)

	// The title payload's own chapter number and date span every language, so
	// they are replaced wholesale by the English listing rather than used as a
	// fallback: a title MangaFire carries only in other languages reports no
	// chapter at all, which leaves a tracker's stored progress untouched instead
	// of advancing it to a chapter that was never translated.
	latestChapter, latestReleaseAt, err := c.latestEnglishChapter(ctx, detail.HID)
	if err != nil {
		return nil, fmt.Errorf("fetch latest %s chapter: %w", chapterLanguage, err)
	}
	result.LatestChapter = latestChapter
	result.LastUpdatedAt = latestReleaseAt

	return &result, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("keyword", query.Raw)
	params.Set("limit", strconv.Itoa(query.Limit))
	var response apiTitlesResponse
	if err := c.fetchAPI(ctx, "/api/titles", params, &response); err != nil {
		return nil, fmt.Errorf("search mangafire titles: %w", err)
	}

	results := make([]connectors.MangaResult, 0, len(response.Items))
	for _, item := range response.Items {
		if strings.TrimSpace(item.HID) == "" {
			continue
		}
		result := c.resultFromAPITitle(item)
		if !query.Matches(append([]string{result.Title, item.Slug}, result.RelatedTitles...)...) {
			continue
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

	hid, slug, err := c.parseTitleURL(rawURL)
	if err != nil {
		return "", err
	}

	// Chapters are paged newest-first, and a number can be uploaded more than
	// once even within one language (a re-release, a second group). All entries
	// sharing a number are contiguous, so we page until one dips *below* the
	// target rather than stopping on the first match that happens to sit at the
	// tail of a page. Recent chapters still resolve in a single page.
	passedTarget := false
	chapters, err := c.fetchChapters(ctx, hid, func(page []apiChapter) bool {
		for i := range page {
			if page[i].Number < chapter {
				passedTarget = true
			}
		}
		return passedTarget
	})
	if err != nil {
		return "", fmt.Errorf("fetch chapters: %w", err)
	}

	match := pickChapterEntry(chapters, chapter)
	if match == nil {
		return "", fmt.Errorf("chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
	}

	if slug == "" {
		detail, detailErr := c.fetchTitleDetail(ctx, hid)
		if detailErr != nil {
			return "", fmt.Errorf("fetch manga detail: %w", detailErr)
		}
		slug = detail.Slug
	}

	return c.HomeURL() + "/title/" + titleKey(hid, slug) + "/chapter/" + strconv.FormatInt(match.ID, 10), nil
}

// Deliberately NOT a connectors.OfflineChapterLinker. Until the SPA rebuild the
// reader URL was derivable from a stored series URL alone
// (/read/{slug}.{hid}/en/chapter-{n}), which let a chapter link be built for a
// reader whose own browser could pass a Cloudflare challenge this server could
// not. That scheme is gone: the site now redirects every /read/... URL to the
// series page and drops the chapter, so a built link silently landed the reader
// on the title — presented, worse, as a confirmed chapter link that badged the
// card and moved its open-to-read button to MangaFire. The current reader URL
// keys on the chapter's numeric id, which only the chapters endpoint knows, so
// there is nothing left to build offline; a chapter here has to be resolved or
// not linked at all.

// parseTitleURL extracts the title hid (and slug when present) from both the
// current /title/{hid}-{slug} URLs and the legacy /manga/{slug}.{hid} and
// /read/{slug}.{hid}/... URLs that existing trackers still have stored.
func (c *Connector) parseTitleURL(rawURL string) (hid string, slug string, err error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", "", err
	}

	segments := connectors.PathSegments(parsed)
	if len(segments) < 2 {
		return "", "", fmt.Errorf("mangafire url must include a title id")
	}

	identifier := strings.TrimSpace(segments[1])
	if identifier == "" {
		return "", "", fmt.Errorf("invalid mangafire title id")
	}

	switch segments[0] {
	case "title":
		if dash := strings.IndexRune(identifier, '-'); dash > 0 {
			return identifier[:dash], identifier[dash+1:], nil
		}
		return identifier, "", nil
	case "manga", "read":
		// Legacy slugs differ from the current API slugs, so only the id after
		// the dot is trusted; callers fetch the canonical slug when needed.
		if dot := strings.LastIndexByte(identifier, '.'); dot > 0 && dot < len(identifier)-1 {
			return identifier[dot+1:], "", nil
		}
		return "", "", fmt.Errorf("legacy mangafire url must match /%s/{slug}.{id}", segments[0])
	default:
		return "", "", fmt.Errorf("unsupported mangafire path")
	}
}

func (c *Connector) fetchTitleDetail(ctx context.Context, hid string) (*apiTitle, error) {
	path := "/api/titles/" + hid
	var response apiTitleDetailResponse
	if err := c.fetchAPI(ctx, path, nil, &response); err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.Data.HID) == "" {
		// An empty record is the API's way of saying the hid names no title;
		// typed as a 404 so callers classify it with IsNotFound.
		return nil, fmt.Errorf("mangafire title %q not found: %w", hid, &connectors.HTTPStatusError{StatusCode: http.StatusNotFound, URL: c.baseURL + path})
	}
	return &response.Data, nil
}

// chaptersPageLimit is the API's maximum page size (larger values return 422).
const chaptersPageLimit = 200

// maxChapterPages bounds how deep fetchChapters will page so a caller without
// an early-exit predicate still terminates instead of hammering the API. In
// practice the caller's request deadline is the tighter bound: resolving a
// chapter far below the latest one is best-effort and falls back to the title
// URL if it can't be reached in time.
const maxChapterPages = 60

// fetchChapters walks the paginated chapters endpoint newest-first
// (sort=number, order=desc), accumulating every page. After each page, stop (if
// non-nil) is consulted with the entries that page contributed; returning true
// ends paging early — used to avoid fetching every page of long series when only
// the latest chapter or a specific recent chapter is needed. Paging also stops
// when the API reports no further pages.
//
// Only chapterLanguage entries are returned. The API is asked to filter and the
// answer is checked again here on purpose: it ignores query params it does not
// recognise rather than rejecting them (`lang=en` silently returns every
// language), so were `language` ever renamed the server-side filter alone would
// quietly go back to mixing languages together.
func (c *Connector) fetchChapters(ctx context.Context, hid string, stop func(page []apiChapter) bool) ([]apiChapter, error) {
	path := "/api/titles/" + hid + "/chapters"
	all := make([]apiChapter, 0, chaptersPageLimit)

	for page := 1; page <= maxChapterPages; page++ {
		params := url.Values{}
		params.Set("sort", "number")
		params.Set("order", "desc")
		params.Set("limit", strconv.Itoa(chaptersPageLimit))
		params.Set("page", strconv.Itoa(page))
		params.Set("language", chapterLanguage)

		var response apiChaptersResponse
		if err := c.fetchAPI(ctx, path, params, &response); err != nil {
			return nil, err
		}

		kept := make([]apiChapter, 0, len(response.Items))
		for _, item := range response.Items {
			if strings.EqualFold(strings.TrimSpace(item.Language), chapterLanguage) {
				kept = append(kept, item)
			}
		}
		all = append(all, kept...)

		if stop != nil && stop(kept) {
			break
		}
		if !response.Meta.HasNext || len(response.Items) == 0 {
			break
		}
	}

	return all, nil
}

// latestEnglishChapter reports the highest English chapter number for a title
// and when it was released. Both come from the chapter listing because the title
// payload can supply neither for a multilingual series (see chapterLanguage).
// A title with no English chapters yields (nil, nil, nil) — absent, not an
// error, and distinct from a fetch that failed.
//
// A number can be uploaded more than once (a re-release, a second group); the
// shared accumulator keeps the first date it sees for the winning number rather
// than the newest upload's, since a re-upload is not the release.
func (c *Connector) latestEnglishChapter(ctx context.Context, hid string) (*float64, *time.Time, error) {
	// The listing is newest-first, so the highest number is on the first page.
	chapters, err := c.fetchChapters(ctx, hid, func([]apiChapter) bool { return true })
	if err != nil {
		return nil, nil, err
	}

	var latest connectors.LatestReading
	for _, entry := range chapters {
		var releaseAt *time.Time
		if entry.CreatedAt > 0 {
			createdAt := time.Unix(entry.CreatedAt, 0).UTC()
			releaseAt = &createdAt
		}
		latest.Add(entry.Number, releaseAt)
	}
	chapter, releaseAt := latest.Result()
	return chapter, releaseAt, nil
}

func (c *Connector) resultFromAPITitle(item apiTitle) connectors.MangaResult {
	key := titleKey(item.HID, item.Slug)
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = connectors.PrettifySlug(item.Slug)
	}

	coverImageURL := ""
	if item.Poster != nil {
		coverImageURL = strings.TrimSpace(item.Poster.Large)
		if coverImageURL == "" {
			coverImageURL = strings.TrimSpace(item.Poster.Medium)
		}
	}

	// LatestChapter and LastUpdatedAt are deliberately left unset: the payload
	// only offers cross-language values (see chapterLanguage). ResolveByURL
	// fills them from the English listing; search results carry no chapter
	// number rather than a number from a language the reader does not follow.
	return connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  key,
		Title:         title,
		RelatedTitles: buildRelatedTitles(title, item.Slug, item.AltTitles),
		URL:           c.HomeURL() + "/title/" + key,
		CoverImageURL: coverImageURL,
	}
}

// pickChapterEntry finds a chapter by number. fetchChapters has already narrowed
// its input to chapterLanguage, so any match is one the reader can open.
func pickChapterEntry(chapters []apiChapter, chapter float64) *apiChapter {
	for index := range chapters {
		if connectors.SameChapter(chapters[index].Number, chapter) {
			return &chapters[index]
		}
	}
	return nil
}

func titleKey(hid string, slug string) string {
	hid = strings.TrimSpace(hid)
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return hid
	}
	return hid + "-" + slug
}

// buildRelatedTitles offers the prettified slug alongside the API's alternate
// titles: the slug is often the romanized form a reader searches by.
func buildRelatedTitles(title string, slug string, altTitles []string) []string {
	candidates := make([]string, 0, len(altTitles)+1)
	candidates = append(candidates, connectors.PrettifySlug(slug))
	candidates = append(candidates, altTitles...)
	return searchutil.RelatedTitles(title, candidates)
}

func (c *Connector) fetchJSON(ctx context.Context, endpoint string, target any) error {
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if remaining, reason := c.CooldownRemaining(); remaining > 0 {
			// The same error the shared throttle returns for an open circuit,
			// so callers classify both breakers identically.
			return fmt.Errorf("mangafire %s: %w", reason, &connectors.SourceCoolingDownError{Host: canonicalHost, RetryAfter: remaining})
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("User-Agent", connectors.BrowserUserAgent)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", c.baseURL+"/")

		// The response is handled by hand rather than through the shared fetch
		// helpers because the error path needs the response headers and body
		// (Retry-After, Cf-Mitigated, the token-rejection message) to classify
		// what kind of 403 this was.
		res, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}

		if res.StatusCode >= 200 && res.StatusCode < 300 {
			rawBody, readErr := connectors.ReadBodyLimited(res.Body, 0)
			res.Body.Close()
			c.breaker.NoteSuccess()
			if readErr != nil {
				return fmt.Errorf("read response body: %w", readErr)
			}
			if err := json.Unmarshal(rawBody, target); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return nil
		}

		statusErr := &connectors.HTTPStatusError{StatusCode: res.StatusCode, URL: endpoint}
		retryAfter := res.Header.Get("Retry-After")
		errBody, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()

		if res.StatusCode == http.StatusForbidden {
			// The API answers 403 {"message":"Missing token."/"Invalid token."}
			// when the vrf token is absent or minted by a stale signer bundle —
			// a signing problem, not an IP block. Surface it distinctly (and
			// point at the refresh runbook) so it is not mistaken for a
			// Cloudflare rate-limit; still back off to avoid hammering a
			// rejection that will not clear until the bundle is refreshed.
			if isTokenRejection(errBody) {
				c.breaker.Trip(2*time.Minute, "signer token rejected (stale signer_bundle.js? see signer_bundle.README.md)")
				return fmt.Errorf("mangafire rejected request token (stale signer_bundle.js? see signer_bundle.README.md): %w", statusErr)
			}
			// A managed Cloudflare challenge is a site-wide configuration
			// change, not an IP block that lapses: waiting does not earn access,
			// only MangaFire turning it off does. Back off far longer than the
			// rate-limit cooldown so polling stays quiet, and name it precisely
			// so it is not misread as a throttle that a retry would clear.
			if isCloudflareChallenge(res.Header, errBody) {
				c.breaker.Trip(30*time.Minute, "Cloudflare browser challenge — the site now requires interactive verification")
				return fmt.Errorf("mangafire is behind a Cloudflare browser challenge and cannot be fetched programmatically: %w", statusErr)
			}
			// Otherwise Cloudflare has rate limited the IP ("Access denied");
			// retrying immediately only extends the block, so open the circuit
			// and fail fast until the cooldown expires.
			c.breaker.Trip(5*time.Minute, "rate limited")
			return statusErr
		}

		if res.StatusCode == http.StatusTooManyRequests {
			if attempt < maxAttempts-1 {
				delay := computeRetryDelay(attempt, retryAfter)
				if delay < 2*time.Second {
					delay = 2 * time.Second
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
				continue
			}
			c.breaker.Trip(2*time.Minute, "rate limited")
			return statusErr
		}

		return statusErr
	}

	return &connectors.HTTPStatusError{StatusCode: http.StatusTooManyRequests, URL: endpoint}
}

// cooldownRelapseWindow is how soon after a cooldown expires a new one must
// open to count as the same outage. It has to outlast a full poll interval
// (30 minutes) plus a cycle's runtime, or the streak would reset between the
// cooldown expiring and the poller's next attempt at the site.
const cooldownRelapseWindow = 90 * time.Minute

// maxCooldown caps the escalation. Long enough that a multi-day challenge
// costs a handful of probes per day, short enough that the connector notices
// the site coming back within one sitting.
const maxCooldown = 6 * time.Hour

// CooldownRemaining implements connectors.CooldownReporter.
func (c *Connector) CooldownRemaining() (time.Duration, string) {
	return c.breaker.Remaining()
}

// isTokenRejection reports whether a 403 body is the API's vrf-token error
// ({"message":"Missing token."} / {"message":"Invalid token."}) rather than a
// Cloudflare IP block, so the two can be surfaced and handled differently.
func isTokenRejection(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "missing token") || strings.Contains(lower, "invalid token")
}

// isCloudflareChallenge reports whether a 403 is Cloudflare's managed-challenge
// interstitial rather than a token rejection or a plain IP block. Cloudflare
// marks these with `Cf-Mitigated: challenge`; the body sniff is a fallback for
// when that header is stripped by an intermediary.
func isCloudflareChallenge(header http.Header, body []byte) bool {
	if strings.EqualFold(strings.TrimSpace(header.Get("Cf-Mitigated")), "challenge") {
		return true
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "just a moment") ||
		strings.Contains(lower, "challenges.cloudflare.com") ||
		strings.Contains(lower, "cf-challenge")
}

func computeRetryDelay(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil {
			if seconds < 0 {
				seconds = 0
			}
			if seconds > 4 {
				seconds = 4
			}
			return time.Duration(seconds) * time.Second
		}
	}

	switch attempt {
	case 0:
		return 350 * time.Millisecond
	case 1:
		return 800 * time.Millisecond
	default:
		return 1500 * time.Millisecond
	}
}
