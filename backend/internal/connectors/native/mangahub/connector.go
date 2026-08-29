// Package mangahub reads MangaHub (mangahub.io) through its open GraphQL API
// at api.mghcdn.com. Measured 2026-08-21 it was exact on 6/7 benchmark series
// with real per-series update timestamps, one behind on the seventh — the
// closest thing to MangaFire freshness after ComicK. The catalog is
// English-only, so its chapter numbers are directly comparable to the
// tracker's (verified live: Tales of Demons and Gods reports 527.6, the
// English number, not a raw count).
//
// The API needs no key for search and manga lookups as long as the request
// carries an Origin of mangahub.io; only the per-chapter reading query is
// gated ("API rate limit excessed"), which is why the reader URL is built
// from the verified scheme /chapter/{slug}/chapter-{number} instead of being
// resolved. Rate-limit and schema errors come back as HTTP 200 with a GraphQL
// errors array, so both layers are checked, and a rate limit found in that
// array is reported to the shared throttle by hand: with no failing status
// code, nothing else in the stack can see it.
//
// Queries used (x:m01 selects the mangahub.io catalog variant):
//   - {search(x:m01,q:"...",limit:N){rows{...}}}   → title search
//   - {manga(x:m01,slug:"..."){...}}               → resolve, one request
package mangahub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const (
	canonicalSiteURL = "https://mangahub.io"
	canonicalAPIURL  = "https://api.mghcdn.com/graphql"
	coverCDNURL      = "https://thumb.mghcdn.com"

	// variantCode selects the mangahub.io catalog in every query; the API also
	// serves sister sites (mangareader, mangapanda) under other codes.
	variantCode = "m01"
)

// maxResponseBytes bounds a GraphQL answer. The payloads here are a search
// page or a single manga record — kilobytes — so this is a safety bound
// against a hostile or runaway response, not a size estimate.
const maxResponseBytes = 4 << 20

// defaultAllowedHosts gates which URLs parseSeriesURL will read a slug out of.
// Only the site domain qualifies: the API (api.mghcdn.com) and cover CDN
// (thumb.mghcdn.com) hosts serve no series pages, so a URL on them is not a
// MangaHub series URL. The SiteInfo claim (Hosts) is deliberately wider — see
// apiClaimHost.
var defaultAllowedHosts = []string{"mangahub.io"}

// apiClaimHost is the API origin, claimed for registry routing only. It sits
// on a different registrable domain from the site, so HostAllowed's subdomain
// rule does not cover it the way it covers api.comick.dev or
// api.mangaupdates.com. The registry used to map it to this connector through
// a hand-maintained switch and now maps hosts solely through SiteInfo, so
// leaving it out of the claim would silently stop "api.mghcdn.com" spellings
// from resolving to MangaHub. Claiming it does not widen parseSeriesURL: that
// still gates on c.allowedHost.
const apiClaimHost = "api.mghcdn.com"

// Search rows are MangaListItem, a slimmer type than Manga: querying
// alternativeTitle on them is a schema error, so only the manga query asks
// for it.
const (
	searchFields = "id title slug image latestChapter updatedDate"
	mangaFields  = searchFields + " alternativeTitle"
)

type Connector struct {
	// siteURL is the origin returned/stored URLs are built on. Unlike the
	// scraping connectors it is configurable rather than hardcoded because the
	// API host (apiURL) and the reader host are distinct; production uses the
	// canonical constants for both.
	siteURL     string
	apiURL      string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		siteURL:     canonicalSiteURL,
		apiURL:      canonicalAPIURL,
		allowedHost: defaultAllowedHosts,
		httpClient:  connectors.NewThrottledClient(),
	}
}

func NewConnectorWithOptions(siteURL string, apiURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = defaultAllowedHosts
	}
	return &Connector{
		siteURL:     strings.TrimRight(strings.TrimSpace(siteURL), "/"),
		apiURL:      strings.TrimSpace(apiURL),
		allowedHost: allowedHost,
		httpClient:  client,
	}
}

func (c *Connector) Key() string {
	return "mangahub"
}

func (c *Connector) Name() string {
	return "MangaHub"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

type apiManga struct {
	ID               json.Number `json:"id"`
	Title            string      `json:"title"`
	Slug             string      `json:"slug"`
	Image            string      `json:"image"`
	LatestChapter    *float64    `json:"latestChapter"`
	UpdatedDate      string      `json:"updatedDate"`
	AlternativeTitle string      `json:"alternativeTitle"`
}

type apiSearch struct {
	Rows []apiManga `json:"rows"`
}

type apiData struct {
	Search *apiSearch `json:"search"`
	Manga  *apiManga  `json:"manga"`
}

type apiError struct {
	Message string `json:"message"`
}

type apiResponse struct {
	Data   apiData    `json:"data"`
	Errors []apiError `json:"errors"`
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.search(ctx, "one piece", 1)
	return err
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	slug, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf("{manga(x:%s,slug:%s){%s}}", variantCode, strconv.Quote(slug), mangaFields)
	response, err := c.queryGraphQL(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("fetch mangahub manga: %w", err)
	}
	manga := response.Data.Manga
	if manga == nil || strings.TrimSpace(manga.Slug) == "" {
		return nil, fmt.Errorf("mangahub manga %q not found", slug)
	}

	result := c.resultFromManga(*manga)
	return &result, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	limit = connectors.ClampSearchLimit(limit)

	rows, err := c.search(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search mangahub titles: %w", err)
	}

	mapped := make([]connectors.MangaResult, 0, len(rows))
	for _, manga := range rows {
		if strings.TrimSpace(manga.Slug) == "" {
			continue
		}
		mapped = append(mapped, c.resultFromManga(manga))
		if len(mapped) >= limit {
			break
		}
	}
	return mapped, nil
}

func (c *Connector) search(ctx context.Context, title string, limit int) ([]apiManga, error) {
	query := fmt.Sprintf("{search(x:%s,q:%s,limit:%d){rows{%s}}}", variantCode, strconv.Quote(title), limit, searchFields)
	response, err := c.queryGraphQL(ctx, query)
	if err != nil {
		return nil, err
	}
	if response.Data.Search == nil {
		return nil, fmt.Errorf("mangahub search returned no payload")
	}
	return response.Data.Search.Rows, nil
}

// ResolveChapterURL checks the requested chapter against the series' latest
// known number and builds the reader URL from the verified scheme. The
// per-chapter GraphQL query cannot be used for this: it is gated behind the
// site's reading quota and answers "API rate limit excessed" to API clients.
func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapter(chapter) {
		return "", fmt.Errorf("invalid chapter")
	}

	slug, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", err
	}

	query := fmt.Sprintf("{manga(x:%s,slug:%s){slug latestChapter}}", variantCode, strconv.Quote(slug))
	response, err := c.queryGraphQL(ctx, query)
	if err != nil {
		return "", fmt.Errorf("fetch mangahub manga: %w", err)
	}
	manga := response.Data.Manga
	if manga == nil || strings.TrimSpace(manga.Slug) == "" {
		return "", fmt.Errorf("mangahub manga %q not found", slug)
	}
	if manga.LatestChapter == nil || chapter > *manga.LatestChapter {
		return "", fmt.Errorf("chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
	}

	return c.chapterURL(slug, chapter), nil
}

// Deliberately NOT an OfflineChapterLinker: that interface makes a built URL
// win its place in the reader-priority chain, which is meant for sites the
// server cannot query at all (MangaFire). MangaHub is queryable, so a chapter
// its own range check refused must fall through to the next site instead of
// becoming a link to a page that does not exist.
func (c *Connector) chapterURL(slug string, chapter float64) string {
	return c.canonicalBaseURL() + "/chapter/" + url.PathEscape(slug) + "/chapter-" + connectors.FormatChapter(chapter)
}

func (c *Connector) resultFromManga(manga apiManga) connectors.MangaResult {
	slug := strings.TrimSpace(manga.Slug)
	title := strings.TrimSpace(manga.Title)

	titleKey := searchutil.Normalize(title)
	related := make([]string, 0, 8)
	for _, candidate := range searchutil.UniqueNonEmpty(strings.Split(manga.AlternativeTitle, ";")) {
		if searchutil.Normalize(candidate) == titleKey {
			continue
		}
		related = append(related, candidate)
	}

	result := connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  strings.TrimSpace(manga.ID.String()),
		Title:         title,
		RelatedTitles: related,
		URL:           c.canonicalBaseURL() + "/manga/" + url.PathEscape(slug),
	}
	if image := strings.TrimSpace(manga.Image); image != "" {
		result.CoverImageURL = coverCDNURL + "/" + strings.TrimLeft(image, "/")
	}
	if manga.LatestChapter != nil && connectors.ValidChapter(*manga.LatestChapter) {
		value := *manga.LatestChapter
		result.LatestChapter = &value
		if parsed := connectors.ParseFirstTime(manga.UpdatedDate, time.RFC3339); parsed != nil {
			result.LastUpdatedAt = parsed
		}
	}
	return result
}

// parseSeriesURL extracts the series slug from a manga or chapter URL
// (/manga/{slug} and /chapter/{slug}/chapter-{n}).
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
		return "", fmt.Errorf("url does not belong to mangahub")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || (segments[0] != "manga" && segments[0] != "chapter") {
		return "", fmt.Errorf("mangahub url must look like /manga/{slug}")
	}

	slug := strings.TrimSpace(segments[1])
	if slug == "" {
		return "", fmt.Errorf("invalid mangahub series slug")
	}
	return slug, nil
}

func (c *Connector) queryGraphQL(ctx context.Context, query string) (*apiResponse, error) {
	payload, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("encode query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The API refuses anonymous origins; presenting the site's own is what
	// stands in for the access key the browser bundle carries.
	req.Header.Set("Origin", canonicalSiteURL)

	var response apiResponse
	if err := connectors.DoJSON(c.httpClient, req, &response, maxResponseBytes); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		message := strings.TrimSpace(response.Errors[0].Message)
		if isRateLimitMessage(message) {
			// The site reports a rate limit as HTTP 200 with this errors array,
			// so the shared throttle — which classifies by status code — books
			// the response as a success and the poller hammers straight through
			// the episode. Stating it outright is the only way the host's
			// circuit opens; the typed 429 lets callers up the chain classify
			// this like any other rate limit.
			if host := c.apiHost(); host != "" {
				connectors.NoteHostRateLimited(host)
			}
			return nil, fmt.Errorf("graphql error: %s: %w", message, &connectors.HTTPStatusError{StatusCode: http.StatusTooManyRequests, URL: c.apiURL})
		}
		return nil, fmt.Errorf("graphql error: %s", message)
	}
	return &response, nil
}

// isRateLimitMessage matches the wording the API uses when it refuses a query
// for quota reasons ("API rate limit excessed" on the reading query), kept
// loose because the same episode has also come back phrased as a plain rate
// limit message.
func isRateLimitMessage(message string) bool {
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "rate limit") || strings.Contains(lowered, "excessed")
}

// apiHost is the host the shared throttle keys its pacing and circuit state on
// (the request URL's hostname), so a rate limit has to be reported against the
// API URL actually in use rather than the canonical constant.
func (c *Connector) apiHost() string {
	parsed, err := url.Parse(c.apiURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func (c *Connector) isAllowedHost(host string) bool {
	return connectors.HostAllowed(host, c.allowedHost)
}

// Hosts implements connectors.SiteInfo: the site domain plus the API origin,
// which the registry must keep routing here (apiClaimHost). Wider than
// c.allowedHost on purpose — routing a URL to this connector and accepting it
// as a series URL are different questions — and returned as a copy so a caller
// cannot reach back into the shared default slice.
func (c *Connector) Hosts() []string {
	hosts := make([]string, 0, len(c.allowedHost)+1)
	hosts = append(hosts, c.allowedHost...)
	if !connectors.HostAllowed(apiClaimHost, hosts) {
		hosts = append(hosts, apiClaimHost)
	}
	return hosts
}

// HomeURL implements connectors.SiteInfo.
func (c *Connector) HomeURL() string {
	return c.canonicalBaseURL()
}

// ReaderRank implements connectors.SiteInfo: MangaHub is a fresh aggregator —
// English-only and same-day on most series — so it outranks the default tier
// without displacing the origin scanlators.
func (c *Connector) ReaderRank() int {
	return connectors.ReaderRankFreshAggregator
}

// canonicalBaseURL is the origin every returned or stored URL is built on.
// Requests never go here — they go to the API host (c.apiURL) — so this is
// purely the reader/site origin the dashboard opens and the tracker stores.
func (c *Connector) canonicalBaseURL() string {
	return c.siteURL
}
