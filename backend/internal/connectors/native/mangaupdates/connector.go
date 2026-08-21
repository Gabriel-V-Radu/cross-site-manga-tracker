package mangaupdates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

// MangaUpdates is a pure tracking source: it hosts no chapters, but its data
// model is exactly what this app needs — individual releases with a chapter
// number and a real date, maintained by the English scanlation scene. What
// matters is knowing that a chapter exists; where to read it is the other
// sources' job.
//
// Its one failure mode: when a series gets licensed and scanlators drop it,
// the release feed freezes at the drop point (measured: series stuck in 2023)
// while the series very much continues. A frozen feed persistently reporting
// a low number would eventually walk a tracker's chapter count backwards, so
// the connector refuses to report a chapter at all when the newest release is
// older than staleReleaseCutoff — no data is recoverable, wrong data is not.
//
// API: JSON under api.mangaupdates.com/v1, no auth for reads; the terms ask
// for reasonable spacing (the shared throttle provides it) and attribution.
// Site URLs carry the series id in zero-padded base36 ("/series/01w7hvo/...")
// while the API only accepts the decoded numeric id.
const (
	canonicalAPIBaseURL = "https://api.mangaupdates.com/v1"
	canonicalSiteURL    = "https://www.mangaupdates.com"

	staleReleaseCutoff = 60 * 24 * time.Hour

	// maxPlausibleChapter mirrors the MangaBuddy guard against data noise.
	maxPlausibleChapter = 10000
)

var (
	seriesPathPattern = regexp.MustCompile(`(?i)^/series/([0-9a-z]+)(?:/|$)`)
	numberPattern     = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
)

type Connector struct {
	apiBaseURL  string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		apiBaseURL:  canonicalAPIBaseURL,
		allowedHost: []string{"mangaupdates.com"},
		httpClient:  connectors.NewThrottledClient(15 * time.Second),
	}
}

func NewConnectorWithOptions(apiBaseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"mangaupdates.com"}
	}
	return &Connector{
		apiBaseURL:  strings.TrimRight(strings.TrimSpace(apiBaseURL), "/"),
		allowedHost: allowedHost,
		httpClient:  client,
	}
}

func (c *Connector) Key() string {
	return "mangaupdates"
}

func (c *Connector) Name() string {
	return "MangaUpdates"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

type apiImage struct {
	URL struct {
		Original string `json:"original"`
		Thumb    string `json:"thumb"`
	} `json:"url"`
}

type apiSeriesRecord struct {
	SeriesID      int64    `json:"series_id"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Image         apiImage `json:"image"`
	Type          string   `json:"type"`
	LatestChapter float64  `json:"latest_chapter"`
	Associated    []struct {
		Title string `json:"title"`
	} `json:"associated"`
}

type apiSearchResponse struct {
	Results []struct {
		Record apiSeriesRecord `json:"record"`
	} `json:"results"`
}

type apiReleasesResponse struct {
	Results []struct {
		Record struct {
			Chapter     string `json:"chapter"`
			ReleaseDate string `json:"release_date"`
		} `json:"record"`
	} `json:"results"`
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	var response apiSearchResponse
	return c.postJSON(ctx, "/series/search", map[string]any{"search": "one piece", "perpage": 1}, &response)
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	seriesID, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return nil, err
	}

	var record apiSeriesRecord
	if err := c.getJSON(ctx, "/series/"+strconv.FormatInt(seriesID, 10), &record); err != nil {
		return nil, fmt.Errorf("fetch mangaupdates series: %w", err)
	}

	latestChapter, releaseAt := c.latestRelease(ctx, seriesID, record.LatestChapter)

	related := make([]string, 0, len(record.Associated))
	for _, associated := range record.Associated {
		related = append(related, associated.Title)
	}

	canonicalURL := strings.TrimSpace(record.URL)
	if canonicalURL == "" {
		canonicalURL = canonicalSiteURL + "/series/" + strconv.FormatInt(seriesID, 36)
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  strconv.FormatInt(seriesID, 10),
		Title:         strings.TrimSpace(record.Title),
		RelatedTitles: searchutil.FilterEnglishAlphabetNames(related),
		URL:           canonicalURL,
		CoverImageURL: strings.TrimSpace(record.Image.URL.Original),
		LatestChapter: latestChapter,
		LastUpdatedAt: releaseAt,
	}, nil
}

// latestRelease reads the newest releases and reduces them to one chapter
// number and its date, applying the staleness guard. recordLatestChapter is
// the series record's own counter, trusted only alongside a fresh feed.
func (c *Connector) latestRelease(ctx context.Context, seriesID int64, recordLatestChapter float64) (*float64, *time.Time) {
	var response apiReleasesResponse
	err := c.postJSON(ctx, "/releases/search", map[string]any{
		"search":      strconv.FormatInt(seriesID, 10),
		"search_type": "series",
		"perpage":     10,
	}, &response)
	if err != nil || len(response.Results) == 0 {
		return nil, nil
	}

	var best *float64
	var bestDate *time.Time
	var newest *time.Time
	for _, result := range response.Results {
		date := parseReleaseDate(result.Record.ReleaseDate)
		if date != nil && (newest == nil || date.After(*newest)) {
			newest = date
		}

		chapter := parseChapterString(result.Record.Chapter)
		if chapter == nil {
			continue
		}
		if best == nil || *chapter > *best {
			best = chapter
			bestDate = date
		}
	}

	// A feed whose newest release is months old is a dropped series, not a
	// slow one worth reporting: the series record keeps counting only while
	// releases keep arriving, so a frozen feed means frozen — and wrong — data.
	if newest == nil || time.Since(*newest) > staleReleaseCutoff {
		return nil, nil
	}

	if best == nil || (recordLatestChapter > *best && recordLatestChapter < maxPlausibleChapter) {
		if recordLatestChapter > 0 && recordLatestChapter < maxPlausibleChapter {
			value := recordLatestChapter
			best = &value
			bestDate = newest
		}
	}
	return best, bestDate
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

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	var response apiSearchResponse
	if err := c.postJSON(ctx, "/series/search", map[string]any{"search": query, "perpage": limit}, &response); err != nil {
		return nil, fmt.Errorf("search mangaupdates: %w", err)
	}

	results := make([]connectors.MangaResult, 0, len(response.Results))
	for _, item := range response.Results {
		record := item.Record
		if !searchutil.MatchesQuery(record.Title, normalizedQuery, queryTokens) {
			continue
		}

		results = append(results, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  strconv.FormatInt(record.SeriesID, 10),
			Title:         strings.TrimSpace(record.Title),
			URL:           strings.TrimSpace(record.URL),
			CoverImageURL: strings.TrimSpace(record.Image.URL.Original),
		})
	}

	return results, nil
}

func (c *Connector) parseSeriesURL(rawURL string) (int64, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return 0, fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return 0, fmt.Errorf("url does not belong to mangaupdates")
	}

	match := seriesPathPattern.FindStringSubmatch(parsed.Path)
	if len(match) < 2 {
		return 0, fmt.Errorf("mangaupdates url must match /series/{id}")
	}

	// Site URLs carry the id in zero-padded base36; old-style URLs carried the
	// decimal id in a query param, but those have redirected for years.
	seriesID, err := strconv.ParseInt(strings.ToLower(match[1]), 36, 64)
	if err != nil || seriesID <= 0 {
		return 0, fmt.Errorf("invalid mangaupdates series id")
	}
	return seriesID, nil
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

func (c *Connector) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	return c.doJSON(req, target)
}

func (c *Connector) postJSON(ctx context.Context, path string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, target)
}

func (c *Connector) doJSON(req *http.Request, target any) error {
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", res.StatusCode)
	}

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// parseChapterString extracts the highest chapter number from a release's
// chapter field, which is free text: "325", "23-24", "17.2", "Oneshot".
func parseChapterString(raw string) *float64 {
	var best *float64
	for _, token := range numberPattern.FindAllString(raw, -1) {
		parsed, err := strconv.ParseFloat(token, 64)
		if err != nil || parsed <= 0 || parsed >= maxPlausibleChapter {
			continue
		}
		if best == nil || parsed > *best {
			value := parsed
			best = &value
		}
	}
	return best
}

func parseReleaseDate(raw string) *time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", trimmed)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}
