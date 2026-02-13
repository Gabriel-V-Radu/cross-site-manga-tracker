package mangaplus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

type Connector struct {
	apiBaseURL  string
	allowedHost []string
	httpClient  *http.Client
}

func NewConnector() *Connector {
	return &Connector{
		apiBaseURL:  "https://jumpg-webapi.tokyo-cdn.com/api",
		allowedHost: []string{"mangaplus.shueisha.co.jp"},
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
		allowedHost = []string{"mangaplus.shueisha.co.jp"}
	}
	return &Connector{apiBaseURL: strings.TrimRight(apiBaseURL, "/"), allowedHost: allowedHost, httpClient: client}
}

func (c *Connector) Key() string {
	return "mangaplus"
}

func (c *Connector) Name() string {
	return "MangaPlus"
}

func (c *Connector) Kind() string {
	return connectors.KindNative
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.fetchAllTitles(ctx)
	if err != nil {
		return fmt.Errorf("fetch titles: %w", err)
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
		return nil, fmt.Errorf("url does not belong to mangaplus")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "titles" {
		return nil, fmt.Errorf("mangaplus url must match /titles/{id}")
	}

	titleID := strings.TrimSpace(segments[1])
	if _, err := strconv.Atoi(titleID); err != nil {
		return nil, fmt.Errorf("invalid mangaplus title id")
	}

	titles, err := c.fetchAllTitles(ctx)
	if err != nil {
		return nil, err
	}

	for _, title := range titles {
		if title.TitleID == titleID {
			return &connectors.MangaResult{
				SourceKey:     c.Key(),
				SourceItemID:  title.TitleID,
				Title:         title.Name,
				URL:           "https://mangaplus.shueisha.co.jp/titles/" + title.TitleID,
				LastUpdatedAt: time.Now().UTC(),
			}, nil
		}
	}

	return nil, fmt.Errorf("mangaplus title not found")
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(strings.ToLower(title))
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}

	if limit <= 0 {
		limit = 10
	}

	titles, err := c.fetchAllTitles(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]connectors.MangaResult, 0)
	for _, title := range titles {
		if strings.Contains(strings.ToLower(title.Name), query) {
			results = append(results, connectors.MangaResult{
				SourceKey:     c.Key(),
				SourceItemID:  title.TitleID,
				Title:         title.Name,
				URL:           "https://mangaplus.shueisha.co.jp/titles/" + title.TitleID,
				LastUpdatedAt: time.Now().UTC(),
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return len(results[i].Title) < len(results[j].Title)
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
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

type mangaPlusTitle struct {
	TitleID string
	Name    string
}

func (c *Connector) fetchAllTitles(ctx context.Context) ([]mangaPlusTitle, error) {
	endpoints := []string{
		c.apiBaseURL + "/title_list/allV2?format=json",
		c.apiBaseURL + "/title_list/all?format=json",
	}

	var lastErr error
	for _, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}

		res, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if res.StatusCode < 200 || res.StatusCode >= 300 {
			lastErr = fmt.Errorf("status %d", res.StatusCode)
			res.Body.Close()
			continue
		}

		var payload mangaPlusTitleListResponse
		err = json.NewDecoder(res.Body).Decode(&payload)
		res.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if len(payload.Success.AllTitlesView.Titles) == 0 {
			lastErr = fmt.Errorf("empty title list")
			continue
		}

		items := make([]mangaPlusTitle, 0, len(payload.Success.AllTitlesView.Titles))
		for _, item := range payload.Success.AllTitlesView.Titles {
			if item.TitleID == 0 || strings.TrimSpace(item.Name) == "" {
				continue
			}
			items = append(items, mangaPlusTitle{
				TitleID: strconv.Itoa(item.TitleID),
				Name:    strings.TrimSpace(item.Name),
			})
		}
		if len(items) > 0 {
			return items, nil
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unable to fetch mangaplus titles")
	}

	return nil, lastErr
}

type mangaPlusTitleListResponse struct {
	Success struct {
		AllTitlesView struct {
			Titles []struct {
				TitleID int    `json:"titleId"`
				Name    string `json:"name"`
			} `json:"titles"`
		} `json:"allTitlesView"`
	} `json:"success"`
}
