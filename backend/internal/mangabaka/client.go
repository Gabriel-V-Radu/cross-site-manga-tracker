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
	// Titles is every name the record carries — main, romanized, and all
	// secondary titles in every language — deduplicated.
	Titles []string
	// MangaUpdatesID is the base36 code MangaUpdates site URLs use
	// ("01w7hvo"), empty when the record has no MangaUpdates entry.
	MangaUpdatesID string
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
		if record.State != "" && record.State != "active" {
			continue
		}

		titles := make([]string, 0, 8)
		titles = append(titles, record.Title, record.RomanizedTitle, record.NativeTitle)
		for _, group := range record.SecondaryTitles {
			for _, secondary := range group {
				titles = append(titles, secondary.Title)
			}
		}

		series = append(series, Series{
			ID:             record.ID,
			Title:          strings.TrimSpace(record.Title),
			Titles:         searchutil.UniqueNonEmpty(titles),
			MangaUpdatesID: strings.TrimSpace(record.Source.MangaUpdates.ID),
		})
	}

	return series, nil
}
