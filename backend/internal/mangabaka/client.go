// Package mangabaka reads the MangaBaka API (api.mangabaka.org), a metadata
// aggregator that merges AniList, MyAnimeList, MangaUpdates, Kitsu,
// Anime-Planet and Shikimori into one record per series. It is not a source —
// its chapter counts just mirror MangaUpdates — but one search request yields
// every alternate title in every language plus the series' cross-site ids,
// which is what turns fuzzy title matching into id lookups during link scans.
//
// The API is open JSON, no auth; search is limited to 30 requests/minute per
// IP, which the shared connector throttle enforces via a host gap override.
package mangabaka

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const canonicalBaseURL = "https://api.mangabaka.org"

// Series is one MangaBaka record reduced to what link matching needs.
type Series struct {
	ID    int64
	Title string
	// Type is the record's kind as MangaBaka spells it ("manga", "manhwa",
	// "novel"…), lowercased; empty when the record does not say.
	Type string
	// Titles is every name the record carries — main, romanized, and all
	// secondary titles in every language — deduplicated.
	Titles []string
	// MangaUpdatesID is the base36 code MangaUpdates site URLs use
	// ("01w7hvo"), empty when the record has no MangaUpdates entry.
	MangaUpdatesID string
}

// IsNovel reports a prose work. A light novel and its manga adaptation
// usually share every title, and a search answers both — the novel first, as
// often as not (Tensei Shitara Slime datta Ken, measured 2026-11-25) — so
// picking by title alone linked trackers to the novel's MangaUpdates entry.
func (s Series) IsNovel() bool {
	return s.Type == "novel"
}

// SeriesIDFromURL reads the record id off a MangaBaka series page URL
// ("https://mangabaka.org/6957/"), the form other sites link to.
func SeriesIDFromURL(rawURL string) (int64, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !connectors.HostAllowed(parsed.Hostname(), []string{"mangabaka.org", "mangabaka.dev"}) {
		return 0, false
	}
	segments := connectors.PathSegments(parsed)
	if len(segments) == 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// MangaUpdatesURL builds the series page URL our MangaUpdates connector can
// resolve, or "" when the record has no MangaUpdates entry.
func (s Series) MangaUpdatesURL() string {
	if s.MangaUpdatesID == "" {
		return ""
	}
	return "https://www.mangaupdates.com/series/" + s.MangaUpdatesID
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		baseURL:    canonicalBaseURL,
		httpClient: connectors.NewThrottledClient(),
	}
}

func NewClientWithOptions(baseURL string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: client,
	}
}

type apiSeries struct {
	ID              int64  `json:"id"`
	State           string `json:"state"`
	MergedWith      *int64 `json:"merged_with"`
	Type            string `json:"type"`
	Title           string `json:"title"`
	NativeTitle     string `json:"native_title"`
	RomanizedTitle  string `json:"romanized_title"`
	SecondaryTitles map[string][]struct {
		Title string `json:"title"`
	} `json:"secondary_titles"`
	Source struct {
		MangaUpdates struct {
			ID string `json:"id"`
		} `json:"manga_updates"`
	} `json:"source"`
}

type apiSearchResponse struct {
	Data []apiSeries `json:"data"`
}

func (c *Client) Search(ctx context.Context, query string, limit int) ([]Series, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 8
	}

	params := url.Values{"q": {trimmed}, "limit": {strconv.Itoa(limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/series/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	var response apiSearchResponse
	if err := connectors.DoJSON(c.httpClient, req, &response, 0); err != nil {
		return nil, err
	}

	series := make([]Series, 0, len(response.Data))
	for _, record := range response.Data {
		// Merged records point elsewhere and duplicate their target's titles.
		if !record.active() {
			continue
		}
		series = append(series, record.series())
	}

	return series, nil
}

type apiSeriesResponse struct {
	Data *apiSeries `json:"data"`
}

// SeriesByID reads one record by id — the id another site's link names, so no
// title has to match. A record merged into another is followed to it once;
// nil with no error means there is no live record.
func (c *Client) SeriesByID(ctx context.Context, id int64) (*Series, error) {
	for hop := 0; hop < 2 && id > 0; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/series/"+strconv.FormatInt(id, 10), nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		var response apiSeriesResponse
		if err := connectors.DoJSON(c.httpClient, req, &response, 0); err != nil {
			if connectors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		record := response.Data
		if record == nil {
			return nil, nil
		}
		if record.active() {
			series := record.series()
			return &series, nil
		}
		if record.MergedWith == nil || *record.MergedWith == id {
			return nil, nil
		}
		id = *record.MergedWith
	}
	return nil, nil
}

func (record apiSeries) active() bool {
	return record.State == "" || record.State == "active"
}

func (record apiSeries) series() Series {
	titles := make([]string, 0, 8)
	titles = append(titles, record.Title, record.RomanizedTitle, record.NativeTitle)
	for _, group := range record.SecondaryTitles {
		for _, secondary := range group {
			titles = append(titles, secondary.Title)
		}
	}
	return Series{
		ID:             record.ID,
		Title:          strings.TrimSpace(record.Title),
		Type:           strings.ToLower(strings.TrimSpace(record.Type)),
		Titles:         searchutil.UniqueNonEmpty(titles),
		MangaUpdatesID: strings.TrimSpace(record.Source.MangaUpdates.ID),
	}
}
