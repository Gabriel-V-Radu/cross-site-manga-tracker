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
// errors array, so both layers are checked.
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

const (
	canonicalSiteURL = "https://mangahub.io"
	canonicalAPIURL  = "https://api.mghcdn.com/graphql"
	coverCDNURL      = "https://thumb.mghcdn.com"

	// variantCode selects the mangahub.io catalog in every query; the API also
	// serves sister sites (mangareader, mangapanda) under other codes.
	variantCode = "m01"
)

// maxPlausibleChapter mirrors the MangaBuddy/WeebCentral guard: a number
// beyond this is data noise, not a release, and must not inflate a tracker.
const maxPlausibleChapter = 10000

// Search rows are MangaListItem, a slimmer type than Manga: querying
// alternativeTitle on them is a schema error, so only the manga query asks
// for it.
const (
	searchFields = "id title slug image latestChapter updatedDate"
	mangaFields  = searchFields + " alternativeTitle"
)

type Connector struct {
	siteURL     string
	apiURL      string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		siteURL:     canonicalSiteURL,
		apiURL:      canonicalAPIURL,
		allowedHost: []string{"mangahub.io"},
		httpClient:  connectors.NewThrottledClient(20 * time.Second),
	}
}

func NewConnectorWithOptions(siteURL string, apiURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"mangahub.io"}
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
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

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
	if !isPlausibleChapter(chapter) {
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
		return "", fmt.Errorf("chapter %s not found", formatChapter(chapter))
	}

	return c.chapterURL(slug, chapter), nil
}

// Deliberately NOT an OfflineChapterLinker: that interface makes a built URL
// win its place in the reader-priority chain, which is meant for sites the
// server cannot query at all (MangaFire). MangaHub is queryable, so a chapter
// its own range check refused must fall through to the next site instead of
// becoming a link to a page that does not exist.
func (c *Connector) chapterURL(slug string, chapter float64) string {
	return c.siteURL + "/chapter/" + url.PathEscape(slug) + "/chapter-" + formatChapter(chapter)
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
		URL:           c.siteURL + "/manga/" + url.PathEscape(slug),
	}
	if image := strings.TrimSpace(manga.Image); image != "" {
		result.CoverImageURL = coverCDNURL + "/" + strings.TrimLeft(image, "/")
	}
	if manga.LatestChapter != nil && isPlausibleChapter(*manga.LatestChapter) {
		value := *manga.LatestChapter
		result.LatestChapter = &value
		if parsed := parseUpdatedDate(manga.UpdatedDate); parsed != nil {
			result.LastUpdatedAt = parsed
		}
	}
	return result
}

func parseUpdatedDate(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		utc := parsed.UTC()
		return &utc
	}
	return nil
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
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// The API refuses anonymous origins; presenting the site's own is what
	// stands in for the access key the browser bundle carries.
	req.Header.Set("Origin", canonicalSiteURL)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status: %d", res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var response apiResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", strings.TrimSpace(response.Errors[0].Message))
	}
	return &response, nil
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

func isPlausibleChapter(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value < maxPlausibleChapter
}

func formatChapter(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
