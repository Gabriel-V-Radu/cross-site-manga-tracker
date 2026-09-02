package mangaupdates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

	// releaseDateLayout is the only shape a release date arrives in; a value
	// in any other shape is no date rather than a guess, because a wrong date
	// would defeat the staleness guard below.
	releaseDateLayout = "2006-01-02"
)

// site is the connector's identity. The API host (api.mangaupdates.com), the
// cover CDN (cdn.mangaupdates.com) and the www site are all subdomains of the
// one claimed domain, which connectors.HostAllowed covers without listing them.
// Home is the production site rather than the API host: apiBaseURL is where
// requests go (a test server in tests) and is the API host besides, while the
// URLs handed back are stored in trackers and opened in a browser. MangaUpdates
// hosts no chapters and resolves no reader links, so it never actually wins a
// turn in the reading chain; the default tier is simply where a site that does
// not claim to be better than the rest belongs.
var site = connectors.Site{
	SiteKey:   "mangaupdates",
	SiteName:  "MangaUpdates",
	SiteHosts: []string{"mangaupdates.com"},
	Home:      canonicalSiteURL,
	Rank:      connectors.ReaderRankDefault,
}

var (
	// seriesIDPattern is the zero-padded base36 id a site URL carries.
	seriesIDPattern = regexp.MustCompile(`(?i)^[0-9a-z]+$`)
	numberPattern   = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
)

// Connector reads MangaUpdates through its JSON API. The embedded Site supplies
// Key, Name, Kind, the SiteInfo methods and the URL helpers; apiBaseURL is
// where requests go (the live API, or a test server).
type Connector struct {
	connectors.Site
	apiBaseURL string
	httpClient *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		Site:       site,
		apiBaseURL: canonicalAPIBaseURL,
		httpClient: connectors.NewThrottledClient(),
	}
}

// NewConnectorWithOptions points the connector at another API base URL (a test
// server), optionally claiming other hosts. A nil client gets the shared
// throttled one, so no caller can construct an unpaced connector by accident.
func NewConnectorWithOptions(apiBaseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = connectors.NewThrottledClient()
	}
	identity := site
	if len(allowedHost) > 0 {
		identity.SiteHosts = allowedHost
	}
	return &Connector{
		Site:       identity,
		apiBaseURL: strings.TrimRight(strings.TrimSpace(apiBaseURL), "/"),
		httpClient: client,
	}
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

	latestChapter, releaseAt, err := c.latestRelease(ctx, seriesID, record.LatestChapter)
	if err != nil {
		return nil, fmt.Errorf("fetch mangaupdates releases: %w", err)
	}

	title := strings.TrimSpace(record.Title)
	associated := make([]string, 0, len(record.Associated))
	for _, entry := range record.Associated {
		associated = append(associated, entry.Title)
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  strconv.FormatInt(seriesID, 10),
		Title:         title,
		RelatedTitles: searchutil.RelatedTitles(title, associated),
		URL:           c.seriesURL(record.URL, seriesID),
		CoverImageURL: strings.TrimSpace(record.Image.URL.Original),
		LatestChapter: latestChapter,
		LastUpdatedAt: releaseAt,
	}, nil
}

// latestRelease reads the newest releases and reduces them to one chapter
// number and its date, applying the staleness guard. recordLatestChapter is
// the series record's own counter, trusted only alongside a fresh feed.
//
// A release search that fails is returned as the error it is. It used to be
// folded into "no releases", which the caller then reported as a series with
// no chapter — a successful resolve as far as the poller could tell, so a
// transient 429 skipped the fallback sources and left no trace in the log.
func (c *Connector) latestRelease(ctx context.Context, seriesID int64, recordLatestChapter float64) (*float64, *time.Time, error) {
	var response apiReleasesResponse
	err := c.postJSON(ctx, "/releases/search", map[string]any{
		"search":      strconv.FormatInt(seriesID, 10),
		"search_type": "series",
		"perpage":     10,
	}, &response)
	if err != nil {
		return nil, nil, err
	}
	if len(response.Results) == 0 {
		return nil, nil, nil
	}

	var latest connectors.LatestReading
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
		latest.Add(*chapter, date)
	}

	// A feed whose newest release is months old is a dropped series, not a
	// slow one worth reporting: the series record keeps counting only while
	// releases keep arriving, so a frozen feed means frozen — and wrong — data.
	if newest == nil || time.Since(*newest) > staleReleaseCutoff {
		return nil, nil, nil
	}

	// The series record's own counter wins only when it is both plausible and
	// ahead of what the releases say: it is maintained by hand and can carry a
	// number the release feed has not spelled out yet.
	best, bestDate := latest.Result()
	if connectors.ValidChapter(recordLatestChapter) && (best == nil || recordLatestChapter > *best) {
		value := recordLatestChapter
		best = &value
		bestDate = newest
	}
	return best, bestDate, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	var response apiSearchResponse
	if err := c.postJSON(ctx, "/series/search", map[string]any{"search": query.Raw, "perpage": query.Limit}, &response); err != nil {
		return nil, fmt.Errorf("search mangaupdates: %w", err)
	}

	results := make([]connectors.MangaResult, 0, len(response.Results))
	for _, item := range response.Results {
		record := item.Record
		if !query.Matches(record.Title) {
			continue
		}

		results = append(results, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  strconv.FormatInt(record.SeriesID, 10),
			Title:         strings.TrimSpace(record.Title),
			URL:           c.seriesURL(record.URL, record.SeriesID),
			CoverImageURL: strings.TrimSpace(record.Image.URL.Original),
		})
	}

	return results, nil
}

// parseSeriesURL checks the URL is this site's and decodes the series id out of
// its /series/{id} path.
func (c *Connector) parseSeriesURL(rawURL string) (int64, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return 0, err
	}

	segments := connectors.PathSegments(parsed)
	if len(segments) < 2 || !strings.EqualFold(segments[0], "series") || !seriesIDPattern.MatchString(segments[1]) {
		return 0, fmt.Errorf("mangaupdates url must match /series/{id}")
	}

	// Site URLs carry the id in zero-padded base36; old-style URLs carried the
	// decimal id in a query param, but those have redirected for years.
	seriesID, err := strconv.ParseInt(strings.ToLower(segments[1]), 36, 64)
	if err != nil || seriesID <= 0 {
		return 0, fmt.Errorf("invalid mangaupdates series id")
	}
	return seriesID, nil
}

// seriesURL prefers the link the API hands out — it carries the zero-padded
// base36 id and the title slug the site itself uses — and falls back to
// building one on the canonical site origin for a record that omits it.
func (c *Connector) seriesURL(recordURL string, seriesID int64) string {
	if trimmed := strings.TrimSpace(recordURL); trimmed != "" {
		return trimmed
	}
	return c.HomeURL() + "/series/" + strconv.FormatInt(seriesID, 36)
}

func (c *Connector) getJSON(ctx context.Context, path string, target any) error {
	return connectors.FetchJSON(ctx, c.httpClient, c.apiBaseURL+path, target)
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
	return connectors.DoJSON(c.httpClient, req, target, 0)
}

// parseChapterString extracts the highest chapter number from a release's
// chapter field, which is free text: "325", "23-24", "17.2", "Oneshot".
func parseChapterString(raw string) *float64 {
	var best *float64
	for _, token := range numberPattern.FindAllString(raw, -1) {
		parsed, err := strconv.ParseFloat(token, 64)
		if err != nil || !connectors.ValidChapter(parsed) {
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
	return connectors.ParseFirstTime(raw, releaseDateLayout)
}
