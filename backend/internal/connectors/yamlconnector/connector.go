package yamlconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

type Connector struct {
	config     Config
	httpClient *http.Client
}

func NewConnector(cfg Config, client *http.Client) (*Connector, error) {
	if err := cfg.normalizeAndValidate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Connector{config: cfg, httpClient: client}, nil
}

func (c *Connector) Key() string {
	return c.config.Key
}

func (c *Connector) Name() string {
	return c.config.Name
}

func (c *Connector) Kind() string {
	return connectors.KindYAML
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	endpoint := c.config.BaseURL + ensurePathPrefix(c.config.HealthPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request health: %w", err)
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
	if len(c.config.AllowedHosts) > 0 && !hostAllowed(parsed.Hostname(), c.config.AllowedHosts) {
		return nil, fmt.Errorf("url does not belong to allowed hosts")
	}

	values := url.Values{}
	values.Set(c.config.Resolve.URLParam, trimmed)
	endpoint := c.config.BaseURL + ensurePathPrefix(c.config.Resolve.Path) + "?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create resolve request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request resolve: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("resolve endpoint status: %d", res.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode resolve payload: %w", err)
	}

	rawItem := getByPath(payload, c.config.Response.ResolveItemPath)
	itemMap, ok := rawItem.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("resolve payload item is invalid")
	}

	item, err := c.mapItem(itemMap)
	if err != nil {
		return nil, err
	}
	item.URL = trimmed
	return &item, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	if limit <= 0 {
		limit = 10
	}

	values := url.Values{}
	values.Set(c.config.Search.QueryParam, query)
	values.Set(c.config.Search.LimitParam, strconv.Itoa(limit))
	endpoint := c.config.BaseURL + ensurePathPrefix(c.config.Search.Path) + "?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request search: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("search endpoint status: %d", res.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode search payload: %w", err)
	}

	rawItems := getByPath(payload, c.config.Response.SearchItemsPath)
	itemList, ok := rawItems.([]any)
	if !ok {
		return nil, fmt.Errorf("search payload items are invalid")
	}

	results := make([]connectors.MangaResult, 0, len(itemList))
	for _, rawItem := range itemList {
		itemMap, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		item, err := c.mapItem(itemMap)
		if err != nil {
			continue
		}
		results = append(results, item)
	}

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func (c *Connector) mapItem(item map[string]any) (connectors.MangaResult, error) {
	id, ok := toString(item[c.config.Response.IDField])
	if !ok || strings.TrimSpace(id) == "" {
		return connectors.MangaResult{}, fmt.Errorf("missing id field")
	}
	title, ok := toString(item[c.config.Response.TitleField])
	if !ok || strings.TrimSpace(title) == "" {
		return connectors.MangaResult{}, fmt.Errorf("missing title field")
	}
	urlValue, ok := toString(item[c.config.Response.URLField])
	if !ok || strings.TrimSpace(urlValue) == "" {
		return connectors.MangaResult{}, fmt.Errorf("missing url field")
	}

	result := connectors.MangaResult{
		SourceKey:    c.Key(),
		SourceItemID: strings.TrimSpace(id),
		Title:        strings.TrimSpace(title),
		URL:          strings.TrimSpace(urlValue),
	}

	if rawChapter, exists := item[c.config.Response.LatestChapterField]; exists {
		if chapter, ok := toFloat(rawChapter); ok {
			result.LatestChapter = &chapter
		}
	}

	if c.config.Response.LastUpdatedField != "" {
		if rawUpdatedAt, exists := item[c.config.Response.LastUpdatedField]; exists {
			if updatedAt, ok := toTime(rawUpdatedAt); ok {
				result.LastUpdatedAt = &updatedAt
			}
		}
	}

	return result, nil
}

func ensurePathPrefix(rawPath string) string {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return ""
	}
	if strings.HasPrefix(rawPath, "/") {
		return rawPath
	}
	return "/" + rawPath
}

func hostAllowed(host string, allowedHosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, allowed := range allowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

func getByPath(input map[string]any, dottedPath string) any {
	dottedPath = strings.TrimSpace(dottedPath)
	if dottedPath == "" {
		return input
	}

	current := any(input)
	for _, segment := range strings.Split(dottedPath, ".") {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[segment]
	}
	return current
}

func toString(input any) (string, bool) {
	switch value := input.(type) {
	case string:
		return value, true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case int:
		return strconv.Itoa(value), true
	default:
		return "", false
	}
}

func toFloat(input any) (float64, bool) {
	switch value := input.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func toTime(input any) (time.Time, bool) {
	switch value := input.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			parsed, err := time.Parse(layout, trimmed)
			if err == nil {
				return parsed.UTC(), true
			}
		}
		return time.Time{}, false
	case float64:
		return fromUnixTimestamp(int64(value))
	case int:
		return fromUnixTimestamp(int64(value))
	case int64:
		return fromUnixTimestamp(value)
	default:
		return time.Time{}, false
	}
}

func fromUnixTimestamp(value int64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value > 1_000_000_000_000 {
		value = value / 1000
	}
	return time.Unix(value, 0).UTC(), true
}
