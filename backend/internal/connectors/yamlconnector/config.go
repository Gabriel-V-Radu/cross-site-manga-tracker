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

	if c.Key == "" {
		return fmt.Errorf("key is required")
	}
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}

	if strings.TrimSpace(c.Search.Path) == "" {
		return fmt.Errorf("search.path is required")
	}
	if strings.TrimSpace(c.Resolve.Path) == "" {
		return fmt.Errorf("resolve.path is required")
	}

	if strings.TrimSpace(c.Search.QueryParam) == "" {
		c.Search.QueryParam = "q"
	}
	if strings.TrimSpace(c.Search.LimitParam) == "" {
		c.Search.LimitParam = "limit"
	}
	if strings.TrimSpace(c.Resolve.URLParam) == "" {
		c.Resolve.URLParam = "url"
	}

	if strings.TrimSpace(c.HealthPath) == "" {
		c.HealthPath = "/health"
	}

	if strings.TrimSpace(c.Response.SearchItemsPath) == "" {
		c.Response.SearchItemsPath = "items"
	}
	if strings.TrimSpace(c.Response.ResolveItemPath) == "" {
		c.Response.ResolveItemPath = "item"
	}
	if strings.TrimSpace(c.Response.IDField) == "" {
		c.Response.IDField = "id"
	}
	if strings.TrimSpace(c.Response.TitleField) == "" {
		c.Response.TitleField = "title"
	}
	if strings.TrimSpace(c.Response.URLField) == "" {
		c.Response.URLField = "url"
	}
	if strings.TrimSpace(c.Response.LatestChapterField) == "" {
		c.Response.LatestChapterField = "latestChapter"
	}

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
