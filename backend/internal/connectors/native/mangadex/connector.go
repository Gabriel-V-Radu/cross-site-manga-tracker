package mangadex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

var titleIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

type Connector struct {
	apiBaseURL  string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		apiBaseURL:  "https://api.mangadex.org",
		allowedHost: []string{"mangadex.org"},
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func NewConnectorWithOptions(apiBaseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if len(allowedHost) == 0 {
		allowedHost = []string{"mangadex.org"}
	}
	return &Connector{apiBaseURL: strings.TrimRight(apiBaseURL, "/"), allowedHost: allowedHost, httpClient: client}
}

func (c *Connector) Key() string {
	return "mangadex"
}

func (c *Connector) Name() string {
	return "MangaDex"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/ping", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request ping: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", res.StatusCode)
	}

	return nil
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return nil, fmt.Errorf("url does not belong to mangadex")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "title" {
		return nil, fmt.Errorf("mangadex url must match /title/{id}")
	}

	titleID := segments[1]
	if !titleIDPattern.MatchString(titleID) {
		return nil, fmt.Errorf("invalid mangadex title id")
	}

	apiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/manga/"+titleID, nil)
	if err != nil {
		return nil, fmt.Errorf("create api request: %w", err)
	}

	res, err := c.httpClient.Do(apiReq)
	if err != nil {
		return nil, fmt.Errorf("request manga by id: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mangadex returned status %d", res.StatusCode)
	}

	var payload mangaByIDResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode mangadex response: %w", err)
	}

	title := pickBestTitle(payload.Data.Attributes.Title)
	if title == "" {
		title = "Untitled"
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  payload.Data.ID,
		Title:         title,
		URL:           trimmed,
		LastUpdatedAt: time.Now().UTC(),
	}, nil
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

	values := url.Values{}
	values.Set("title", query)
	values.Set("limit", fmt.Sprintf("%d", limit))

	searchURL := c.apiBaseURL + "/manga?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mangadex returned status %d", res.StatusCode)
	}

	var payload mangaSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	items := make([]connectors.MangaResult, 0, len(payload.Data))
	for _, item := range payload.Data {
		bestTitle := pickBestTitle(item.Attributes.Title)
		if bestTitle == "" {
			bestTitle = "Untitled"
		}
		items = append(items, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  item.ID,
			Title:         bestTitle,
			URL:           "https://mangadex.org/title/" + item.ID,
			LastUpdatedAt: time.Now().UTC(),
		})
	}

	return items, nil
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

func pickBestTitle(titleMap map[string]string) string {
	if titleMap == nil {
		return ""
	}
	for _, key := range []string{"en", "ja-ro", "ja", "pt-br", "es"} {
		if value := strings.TrimSpace(titleMap[key]); value != "" {
			return value
		}
	}
	for _, value := range titleMap {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type mangaByIDResponse struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			Title map[string]string `json:"title"`
		} `json:"attributes"`
	} `json:"data"`
}

type mangaSearchResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Title map[string]string `json:"title"`
		} `json:"attributes"`
	} `json:"data"`
}
