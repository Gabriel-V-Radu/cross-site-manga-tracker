package yamlconnector

import (
	"fmt"
	"strings"
)

type Config struct {
	Key          string   `yaml:"key"`
	Name         string   `yaml:"name"`
	Enabled      *bool    `yaml:"enabled"`
	BaseURL      string   `yaml:"base_url"`
	AllowedHosts []string `yaml:"allowed_hosts"`
	HealthPath   string   `yaml:"health_path"`
	Search       struct {
		Path       string `yaml:"path"`
		QueryParam string `yaml:"query_param"`
		LimitParam string `yaml:"limit_param"`
	} `yaml:"search"`
	Resolve struct {
		Path     string `yaml:"path"`
		URLParam string `yaml:"url_param"`
	} `yaml:"resolve"`
	Response struct {
		SearchItemsPath    string `yaml:"search_items_path"`
		ResolveItemPath    string `yaml:"resolve_item_path"`
		IDField            string `yaml:"id_field"`
		TitleField         string `yaml:"title_field"`
		URLField           string `yaml:"url_field"`
		LatestChapterField string `yaml:"latest_chapter_field"`
		LastUpdatedField   string `yaml:"last_updated_field"`
	} `yaml:"response"`
}

func (c *Config) normalizeAndValidate() error {
	c.Key = strings.TrimSpace(c.Key)
	c.Name = strings.TrimSpace(c.Name)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Search.Path = ensurePathPrefix(c.Search.Path)
	c.Resolve.Path = ensurePathPrefix(c.Resolve.Path)

	if c.Key == "" {
		return fmt.Errorf("key is required")
	}
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}

	if c.Search.Path == "" {
		return fmt.Errorf("search.path is required")
	}
	if c.Resolve.Path == "" {
		return fmt.Errorf("resolve.path is required")
	}

	c.Search.QueryParam = strings.TrimSpace(c.Search.QueryParam)
	if c.Search.QueryParam == "" {
		c.Search.QueryParam = "q"
	}
	c.Search.LimitParam = strings.TrimSpace(c.Search.LimitParam)
	if c.Search.LimitParam == "" {
		c.Search.LimitParam = "limit"
	}
	c.Resolve.URLParam = strings.TrimSpace(c.Resolve.URLParam)
	if c.Resolve.URLParam == "" {
		c.Resolve.URLParam = "url"
	}

	c.HealthPath = ensurePathPrefix(c.HealthPath)
	if c.HealthPath == "" {
		c.HealthPath = "/health"
	}

	c.Response.SearchItemsPath = strings.TrimSpace(c.Response.SearchItemsPath)
	if c.Response.SearchItemsPath == "" {
		c.Response.SearchItemsPath = "items"
	}
	c.Response.ResolveItemPath = strings.TrimSpace(c.Response.ResolveItemPath)
	if c.Response.ResolveItemPath == "" {
		c.Response.ResolveItemPath = "item"
	}
	c.Response.IDField = strings.TrimSpace(c.Response.IDField)
	if c.Response.IDField == "" {
		c.Response.IDField = "id"
	}
	c.Response.TitleField = strings.TrimSpace(c.Response.TitleField)
	if c.Response.TitleField == "" {
		c.Response.TitleField = "title"
	}
	c.Response.URLField = strings.TrimSpace(c.Response.URLField)
	if c.Response.URLField == "" {
		c.Response.URLField = "url"
	}
	c.Response.LatestChapterField = strings.TrimSpace(c.Response.LatestChapterField)
	if c.Response.LatestChapterField == "" {
		c.Response.LatestChapterField = "latestChapter"
	}
	c.Response.LastUpdatedField = strings.TrimSpace(c.Response.LastUpdatedField)

	normalizedHosts := make([]string, 0, len(c.AllowedHosts))
	seenHosts := make(map[string]struct{}, len(c.AllowedHosts))
	for _, rawHost := range c.AllowedHosts {
		host := strings.ToLower(strings.TrimSpace(rawHost))
		if host == "" {
			continue
		}
		if _, exists := seenHosts[host]; exists {
			continue
		}
		seenHosts[host] = struct{}{}
		normalizedHosts = append(normalizedHosts, host)
	}
	c.AllowedHosts = normalizedHosts

	if len(c.AllowedHosts) == 0 {
		c.AllowedHosts = []string{}
	}

	return nil
}

func (c *Config) isEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}
