package mangafire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// MangaFire rebuilt their site as a SPA backed by a JSON API under /api.
// Manga pages moved from /manga/{slug}.{hid} to /title/{hid}-{slug} and
// reader pages from /read/{slug}.{hid}/{lang}/chapter-{n} to /title/{hid}-{slug}/{chapterId}.
var relativeAgoPattern = regexp.MustCompile(`(?i)^(\d+)\s*(min|mins|mo|mos|m|hrs|hr|h|d|w|yrs|yr|y)\s+ago$`)

type latestReleaseMemo struct {
	latestChapter float64
	releaseAt     *time.Time
}

type Connector struct {
	baseURL     string
	allowedHost []string
	httpClient  *http.Client
	signer      *signer

	requestMu          sync.Mutex
	nextAllowedRequest time.Time
	minRequestInterval time.Duration
	cooldownUntil      time.Time
	cooldownReason     string

	releaseMemoMu sync.Mutex
	releaseMemo   map[string]latestReleaseMemo
}

func NewConnector() *Connector {
	return &Connector{
		baseURL:     "https://mangafire.to",
		allowedHost: []string{"mangafire.to"},
		httpClient: &http.Client{
			Timeout: 12 * time.Second,
		},
		signer: newSigner(),
		// Cloudflare on mangafire.to blocks IPs that burst requests, so the
		// live connector paces itself much more conservatively than the
		// local test servers need.
		minRequestInterval: 1500 * time.Millisecond,
		releaseMemo:        map[string]latestReleaseMemo{},
	}
}

func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"mangafire.to"}
	}
	return &Connector{
		baseURL:            strings.TrimRight(baseURL, "/"),
		allowedHost:        allowedHost,
		httpClient:         client,
		signer:             newSigner(),
		minRequestInterval: 150 * time.Millisecond,
		releaseMemo:        map[string]latestReleaseMemo{},
	}
}

func (c *Connector) Key() string {
	return "mangafire"
}

func (c *Connector) Name() string {
	return "MangaFire"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

type apiPoster struct {
	Small  string `json:"small"`
	Medium string `json:"medium"`
	Large  string `json:"large"`
}

type apiTitle struct {
	HID              string     `json:"hid"`
	Slug             string     `json:"slug"`
	Title            string     `json:"title"`
	Poster           *apiPoster `json:"poster"`
	LatestChapter    *float64   `json:"latestChapter"`
	ChapterUpdatedAt string     `json:"chapterUpdatedAt"`
	AltTitles        []string   `json:"altTitles"`
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
	if latestReleaseAt := c.fetchLatestReleaseAt(ctx, detail.HID, detail.LatestChapter); latestReleaseAt != nil {
		result.LastUpdatedAt = latestReleaseAt
	}
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
	params.Set("keyword", query)
	params.Set("limit", strconv.Itoa(limit))
	var response apiTitlesResponse
	if err := c.fetchAPI(ctx, "/api/titles", params, &response); err != nil {
		return nil, fmt.Errorf("search mangafire titles: %w", err)
	}

	results := make([]connectors.MangaResult, 0, len(response.Items))
	for _, item := range response.Items {
		if strings.TrimSpace(item.HID) == "" {
			continue
		}
		results = append(results, c.resultFromAPITitle(item))
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

	hid, slug, err := c.parseTitleURL(rawURL)
	if err != nil {
		return "", err
	}

	// Chapters are paged newest-first, and a single chapter number can appear
	// once per language. All entries sharing a number are contiguous, so we
	// page until one dips *below* the target: only then is every language
	// variant of the target guaranteed fetched, letting pickChapterEntry prefer
	// the English one rather than latching onto a variant that happens to sit at
	// the tail of a page. Recent chapters still resolve in a single page.
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
		return "", fmt.Errorf("chapter %s not found", strconv.FormatFloat(chapter, 'f', -1, 64))
	}

	if slug == "" {
		detail, detailErr := c.fetchTitleDetail(ctx, hid)
		if detailErr != nil {
			return "", fmt.Errorf("fetch manga detail: %w", detailErr)
		}
		slug = detail.Slug
	}

	return "https://mangafire.to/title/" + titleKey(hid, slug) + "/" + strconv.FormatInt(match.ID, 10), nil
}

// parseTitleURL extracts the title hid (and slug when present) from both the
// current /title/{hid}-{slug} URLs and the legacy /manga/{slug}.{hid} and
// /read/{slug}.{hid}/... URLs that existing trackers still have stored.
func (c *Connector) parseTitleURL(rawURL string) (hid string, slug string, err error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", "", fmt.Errorf("url is required")
	}

	parsed, parseErr := url.Parse(trimmed)
	if parseErr != nil {
		return "", "", fmt.Errorf("invalid url: %w", parseErr)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return "", "", fmt.Errorf("url does not belong to mangafire")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
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
	var response apiTitleDetailResponse
	if err := c.fetchAPI(ctx, "/api/titles/"+hid, nil, &response); err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.Data.HID) == "" {
		return nil, fmt.Errorf("mangafire title %q not found", hid)
	}
	return &response.Data, nil
}

// chaptersPageLimit is the API's maximum page size (larger values return 422).
const chaptersPageLimit = 200

// maxChapterPages bounds how deep fetchChapters will page so a caller without
// an early-exit predicate still terminates instead of hammering the API. Note
// this bounds fetched *entries* (60 * 200 = 12k), and a chapter number yields
// one entry per language, so the reachable chapter depth is lower for
// multilingual series. In practice the caller's request deadline is the tighter
// bound: resolving a chapter far below the latest one is best-effort and falls
// back to the title URL if it can't be reached in time.
const maxChapterPages = 60

// fetchChapters walks the paginated chapters endpoint newest-first
// (sort=number, order=desc), accumulating every page. After each page, stop (if
// non-nil) is consulted with the page just fetched; returning true ends paging
// early — used to avoid fetching every page of long series when only the latest
// chapter or a specific recent chapter is needed. Paging also stops when the API
// reports no further pages.
func (c *Connector) fetchChapters(ctx context.Context, hid string, stop func(page []apiChapter) bool) ([]apiChapter, error) {
	path := "/api/titles/" + hid + "/chapters"
	all := make([]apiChapter, 0, chaptersPageLimit)

	for page := 1; page <= maxChapterPages; page++ {
		params := url.Values{}
		params.Set("sort", "number")
		params.Set("order", "desc")
		params.Set("limit", strconv.Itoa(chaptersPageLimit))
		params.Set("page", strconv.Itoa(page))

		var response apiChaptersResponse
		if err := c.fetchAPI(ctx, path, params, &response); err != nil {
			return nil, err
		}
		all = append(all, response.Items...)

		if stop != nil && stop(response.Items) {
			break
		}
		if !response.Meta.HasNext || len(response.Items) == 0 {
			break
		}
	}

	return all, nil
}

// fetchLatestReleaseAt looks up the exact release timestamp of the latest
// chapter; the title payload only carries a coarse relative time ("2d ago").
// The result is memoized per title until the latest chapter number changes,
// so repeated polls cost one request instead of two.
func (c *Connector) fetchLatestReleaseAt(ctx context.Context, hid string, latestChapter *float64) *time.Time {
	if latestChapter == nil {
		return nil
	}

	c.releaseMemoMu.Lock()
	memo, memoized := c.releaseMemo[hid]
	c.releaseMemoMu.Unlock()
	if memoized && sameChapterNumber(memo.latestChapter, *latestChapter) {
		return memo.releaseAt
	}

	// The latest chapter sits on the first page of the newest-first listing, so
	// a single page is enough to read its release timestamp.
	chapters, err := c.fetchChapters(ctx, hid, func([]apiChapter) bool { return true })
	if err != nil {
		return nil
	}

	var latest *time.Time
	for _, entry := range chapters {
		if !sameChapterNumber(entry.Number, *latestChapter) || entry.CreatedAt <= 0 {
			continue
		}
		createdAt := time.Unix(entry.CreatedAt, 0).UTC()
		if latest == nil || createdAt.After(*latest) {
			latest = &createdAt
		}
	}

	c.releaseMemoMu.Lock()
	c.releaseMemo[hid] = latestReleaseMemo{
		latestChapter: *latestChapter,
		releaseAt:     latest,
	}
	c.releaseMemoMu.Unlock()

	return latest
}

func (c *Connector) resultFromAPITitle(item apiTitle) connectors.MangaResult {
	key := titleKey(item.HID, item.Slug)
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = prettifySlug(item.Slug)
	}

	coverImageURL := ""
	if item.Poster != nil {
		coverImageURL = strings.TrimSpace(item.Poster.Large)
		if coverImageURL == "" {
			coverImageURL = strings.TrimSpace(item.Poster.Medium)
		}
	}

	return connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  key,
		Title:         title,
		RelatedTitles: buildRelatedTitles(title, item.Slug, item.AltTitles),
		URL:           "https://mangafire.to/title/" + key,
		CoverImageURL: coverImageURL,
		LatestChapter: item.LatestChapter,
		LastUpdatedAt: parseRelativeUpdatedAt(item.ChapterUpdatedAt, time.Now().UTC()),
	}
}

func pickChapterEntry(chapters []apiChapter, chapter float64) *apiChapter {
	var fallback *apiChapter
	for index := range chapters {
		entry := &chapters[index]
		if !sameChapterNumber(entry.Number, chapter) {
			continue
		}
		if strings.EqualFold(entry.Language, "en") {
			return entry
		}
		if fallback == nil {
			fallback = entry
		}
	}
	return fallback
}

func sameChapterNumber(a float64, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func titleKey(hid string, slug string) string {
	hid = strings.TrimSpace(hid)
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return hid
	}
	return hid + "-" + slug
}

func buildRelatedTitles(title string, slug string, altTitles []string) []string {
	candidates := make([]string, 0, len(altTitles)+1)
	candidates = append(candidates, prettifySlug(slug))
	candidates = append(candidates, searchutil.FilterEnglishAlphabetNames(altTitles)...)
	candidates = searchutil.UniqueNonEmpty(candidates)

	titleKey := searchutil.Normalize(title)
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateKey := searchutil.Normalize(candidate)
		if candidateKey == "" {
			continue
		}
		if titleKey != "" && candidateKey == titleKey {
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

// parseRelativeUpdatedAt parses the API's coarse relative timestamps such as
// "just now", "30m ago", "5h ago", "2d ago", "3w ago", "1mo ago", "1yr ago".
func parseRelativeUpdatedAt(raw string, now time.Time) *time.Time {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return nil
	}
	if trimmed == "just now" {
		result := now
		return &result
	}

	match := relativeAgoPattern.FindStringSubmatch(trimmed)
	if len(match) < 3 {
		return nil
	}
	quantity, err := strconv.Atoi(match[1])
	if err != nil || quantity < 0 {
		return nil
	}

	result := now
	switch match[2] {
	case "m", "min", "mins":
		result = result.Add(-time.Duration(quantity) * time.Minute)
	case "h", "hr", "hrs":
		result = result.Add(-time.Duration(quantity) * time.Hour)
	case "d":
		result = result.AddDate(0, 0, -quantity)
	case "w":
		result = result.AddDate(0, 0, -7*quantity)
	case "mo", "mos":
		result = result.AddDate(0, -quantity, 0)
	case "y", "yr", "yrs":
		result = result.AddDate(-quantity, 0, 0)
	default:
		return nil
	}
	return &result
}

func (c *Connector) fetchJSON(ctx context.Context, endpoint string, target any) error {
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if remaining, reason := c.cooldownRemaining(); remaining > 0 {
			return fmt.Errorf("mangafire %s, cooling down for %s: %w", reason, remaining.Round(time.Second), &httpStatusError{StatusCode: http.StatusTooManyRequests})
		}

		if err := c.waitForRequestWindow(ctx); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", c.baseURL+"/")

		res, err := c.httpClient.Do(req)
		if err != nil {
			c.deferRequests(c.minRequestInterval)
			return fmt.Errorf("request failed: %w", err)
		}

		if res.StatusCode >= 200 && res.StatusCode < 300 {
			rawBody, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			c.deferRequests(c.minRequestInterval)
			if readErr != nil {
				return fmt.Errorf("read response body: %w", readErr)
			}
			if err := json.Unmarshal(rawBody, target); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return nil
		}

		statusErr := &httpStatusError{StatusCode: res.StatusCode}
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
				c.startCooldown(2*time.Minute, "signer token rejected (stale signer_bundle.js? see signer_bundle.README.md)")
				return fmt.Errorf("mangafire rejected request token (stale signer_bundle.js? see signer_bundle.README.md): %w", statusErr)
			}
			// Otherwise Cloudflare has rate limited the IP ("Access denied");
			// retrying immediately only extends the block, so open the circuit
			// and fail fast until the cooldown expires.
			c.startCooldown(5*time.Minute, "rate limited")
			return statusErr
		}

		if res.StatusCode == http.StatusTooManyRequests {
			if attempt < maxAttempts-1 {
				delay := computeRetryDelay(attempt, retryAfter)
				if delay < 2*time.Second {
					delay = 2 * time.Second
				}
				c.deferRequests(delay)
				continue
			}
			c.startCooldown(2*time.Minute, "rate limited")
			return statusErr
		}

		c.deferRequests(c.minRequestInterval)

		return statusErr
	}

	return &httpStatusError{StatusCode: http.StatusTooManyRequests}
}

func (c *Connector) cooldownRemaining() (time.Duration, string) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	return time.Until(c.cooldownUntil), c.cooldownReason
}

func (c *Connector) startCooldown(duration time.Duration, reason string) {
	until := time.Now().UTC().Add(duration)
	c.requestMu.Lock()
	if until.After(c.cooldownUntil) {
		c.cooldownUntil = until
		c.cooldownReason = reason
	}
	c.requestMu.Unlock()
}

// isTokenRejection reports whether a 403 body is the API's vrf-token error
// ({"message":"Missing token."} / {"message":"Invalid token."}) rather than a
// Cloudflare IP block, so the two can be surfaced and handled differently.
func isTokenRejection(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "missing token") || strings.Contains(lower, "invalid token")
}

func (c *Connector) waitForRequestWindow(ctx context.Context) error {
	for {
		c.requestMu.Lock()
		nextAllowed := c.nextAllowedRequest
		c.requestMu.Unlock()

		now := time.Now().UTC()
		if !nextAllowed.After(now) {
			return nil
		}

		wait := time.Until(nextAllowed)
		if wait <= 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Connector) deferRequests(delay time.Duration) {
	if delay <= 0 {
		delay = c.minRequestInterval
	}

	next := time.Now().UTC().Add(delay)
	c.requestMu.Lock()
	if next.After(c.nextAllowedRequest) {
		c.nextAllowedRequest = next
	}
	c.requestMu.Unlock()
}

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("unexpected status: %d", e.StatusCode)
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
