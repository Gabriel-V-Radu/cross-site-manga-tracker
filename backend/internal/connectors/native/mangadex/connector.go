package mangadex

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
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

	values := url.Values{}
	values.Add("includes[]", "cover_art")
	mangaURL := c.apiBaseURL + "/manga/" + titleID + "?" + values.Encode()
	apiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, mangaURL, nil)
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
	relatedTitles := collectEnglishRelatedTitles(title, payload.Data.Attributes.Title, payload.Data.Attributes.AltTitles)
	if title == "" {
		if len(relatedTitles) > 0 {
			title = relatedTitles[0]
		} else {
			title = "Untitled"
		}
	}
	relatedTitles = removePrimaryTitle(relatedTitles, title)

	latestChapter := parseChapterNumber(payload.Data.Attributes.LastChapter)
	feedLatestChapter, latestReleaseAt, _ := c.fetchLatestChapterFromFeed(ctx, titleID)
	if latestChapter == nil {
		latestChapter = feedLatestChapter
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  payload.Data.ID,
		Title:         title,
		RelatedTitles: relatedTitles,
		URL:           trimmed,
		CoverImageURL: pickCoverImageURL(payload.Data.ID, payload.Data.Relationships),
		LatestChapter: latestChapter,
		LastUpdatedAt: latestReleaseAt,
	}, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query := strings.TrimSpace(title)
	if query == "" {
		return nil, fmt.Errorf("title is required")
	}
	normalizedQuery := searchutil.Normalize(query)
	queryTokens := searchutil.Tokenize(query)
	if normalizedQuery == "" || len(queryTokens) == 0 {
		return nil, fmt.Errorf("title is required")
	}

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	requestLimit := limit * 4
	if requestLimit < limit {
		requestLimit = limit
	}
	if requestLimit > 50 {
		requestLimit = 50
	}

	values := url.Values{}
	values.Set("title", query)
	values.Set("limit", fmt.Sprintf("%d", requestLimit))
	values.Add("includes[]", "cover_art")

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

	items := make([]connectors.MangaResult, 0, min(limit, len(payload.Data)))
	for _, item := range payload.Data {
		bestTitle := pickBestTitle(item.Attributes.Title)
		englishRelatedTitles := collectEnglishRelatedTitles(bestTitle, item.Attributes.Title, item.Attributes.AltTitles)
		if bestTitle == "" {
			if len(englishRelatedTitles) > 0 {
				bestTitle = englishRelatedTitles[0]
			} else {
				bestTitle = "Untitled"
			}
		}
		englishRelatedTitles = removePrimaryTitle(englishRelatedTitles, bestTitle)

		searchNames := make([]string, 0, 1+len(englishRelatedTitles))
		searchNames = append(searchNames, bestTitle)
		searchNames = append(searchNames, englishRelatedTitles...)
		if !searchutil.AnyCandidateMatches(searchNames, normalizedQuery, queryTokens) {
			continue
		}

		latestChapter := parseChapterNumber(item.Attributes.LastChapter)
		if latestChapter == nil {
			latestChapter, _, _ = c.fetchLatestChapterFromFeed(ctx, item.ID)
		}

		items = append(items, connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  item.ID,
			Title:         bestTitle,
			RelatedTitles: englishRelatedTitles,
			URL:           "https://mangadex.org/title/" + item.ID,
			CoverImageURL: pickCoverImageURL(item.ID, item.Relationships),
			LatestChapter: latestChapter,
		})

		if len(items) >= limit {
			break
		}
	}

	return items, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if math.IsNaN(chapter) || math.IsInf(chapter, 0) || chapter <= 0 {
		return "", fmt.Errorf("invalid chapter")
	}

	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if !c.isAllowedHost(parsed.Hostname()) {
		return "", fmt.Errorf("url does not belong to mangadex")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "title" {
		return "", fmt.Errorf("mangadex url must match /title/{id}")
	}

	titleID := strings.TrimSpace(segments[1])
	if !titleIDPattern.MatchString(titleID) {
		return "", fmt.Errorf("invalid mangadex title id")
	}

	values := url.Values{}
	values.Set("limit", "500")
	values.Set("offset", "0")
	values.Set("order[chapter]", "desc")
	values.Set("includeExternalUrl", "0")
	values.Add("translatedLanguage[]", "en")
	values.Add("contentRating[]", "safe")
	values.Add("contentRating[]", "suggestive")
	values.Add("contentRating[]", "erotica")
	values.Add("contentRating[]", "pornographic")

	feedURL := c.apiBaseURL + "/manga/" + titleID + "/feed?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return "", fmt.Errorf("create feed request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request feed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("mangadex feed returned status %d", res.StatusCode)
	}

	var payload mangaFeedResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode feed response: %w", err)
	}

	for _, chapterItem := range payload.Data {
		parsedChapter := parseChapterNumber(chapterItem.Attributes.Chapter)
		if parsedChapter == nil {
			continue
		}
		if math.Abs(*parsedChapter-chapter) > 1e-9 {
			continue
		}

		chapterID := strings.TrimSpace(chapterItem.ID)
		if chapterID == "" {
			continue
		}

		return "https://mangadex.org/chapter/" + chapterID, nil
	}

	return "", fmt.Errorf("chapter %.3f not found", chapter)
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

func collectEnglishRelatedTitles(primaryTitle string, titleMap map[string]string, altTitles []map[string]string) []string {
	candidates := make([]string, 0, len(titleMap)+(len(altTitles)*2))
	for _, value := range titleMap {
		candidates = append(candidates, value)
	}
	for _, altTitleMap := range altTitles {
		for _, value := range altTitleMap {
			candidates = append(candidates, value)
		}
	}

	filtered := searchutil.FilterEnglishAlphabetNames(candidates)
	if len(filtered) == 0 {
		return nil
	}

	primaryKey := searchutil.Normalize(primaryTitle)
	if primaryKey == "" {
		return filtered
	}

	relatedOnly := make([]string, 0, len(filtered))
	for _, candidate := range filtered {
		if searchutil.Normalize(candidate) == primaryKey {
			continue
		}
		relatedOnly = append(relatedOnly, candidate)
	}
	if len(relatedOnly) == 0 {
		return nil
	}

	return relatedOnly
}

func removePrimaryTitle(values []string, primaryTitle string) []string {
	if len(values) == 0 {
		return nil
	}

	primaryKey := searchutil.Normalize(primaryTitle)
	if primaryKey == "" {
		return values
	}

	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if searchutil.Normalize(value) == primaryKey {
			continue
		}
		filtered = append(filtered, value)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func pickCoverImageURL(mangaID string, relationships []mangaRelationship) string {
	for _, relationship := range relationships {
		if relationship.Type != "cover_art" {
			continue
		}
		fileName := strings.TrimSpace(relationship.Attributes.FileName)
		if fileName == "" {
			continue
		}
		return "https://uploads.mangadex.org/covers/" + mangaID + "/" + fileName + ".256.jpg"
	}

	return ""
}

func parseChapterNumber(raw string) *float64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}

	return &parsed
}

func (c *Connector) fetchLatestChapterFromFeed(ctx context.Context, mangaID string) (*float64, *time.Time, error) {
	if strings.TrimSpace(mangaID) == "" {
		return nil, nil, nil
	}

	values := url.Values{}
	values.Set("limit", "100")
	values.Set("offset", "0")
	values.Set("order[chapter]", "desc")
	values.Set("includeExternalUrl", "0")
	values.Add("translatedLanguage[]", "en")
	values.Add("contentRating[]", "safe")
	values.Add("contentRating[]", "suggestive")
	values.Add("contentRating[]", "erotica")
	values.Add("contentRating[]", "pornographic")

	feedURL := c.apiBaseURL + "/manga/" + mangaID + "/feed?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create feed request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request feed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("mangadex feed returned status %d", res.StatusCode)
	}

	var payload mangaFeedResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, nil, fmt.Errorf("decode feed response: %w", err)
	}

	var latest *float64
	var latestReleaseAt *time.Time
	for _, chapter := range payload.Data {
		parsed := parseChapterNumber(chapter.Attributes.Chapter)
		if parsed == nil {
			continue
		}
		if latest == nil || *parsed > *latest {
			latest = parsed
			latestReleaseAt = parseOptionalRFC3339Time(
				chapter.Attributes.PublishAt,
				chapter.Attributes.ReadableAt,
				chapter.Attributes.CreatedAt,
			)
		}
	}

	return latest, latestReleaseAt, nil
}

func parseOptionalRFC3339Time(values ...string) *time.Time {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, trimmed)
		if err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

type mangaByIDResponse struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			Title       map[string]string   `json:"title"`
			AltTitles   []map[string]string `json:"altTitles"`
			LastChapter string              `json:"lastChapter"`
		} `json:"attributes"`
		Relationships []mangaRelationship `json:"relationships"`
	} `json:"data"`
}

type mangaSearchResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Title       map[string]string   `json:"title"`
			AltTitles   []map[string]string `json:"altTitles"`
			LastChapter string              `json:"lastChapter"`
		} `json:"attributes"`
		Relationships []mangaRelationship `json:"relationships"`
	} `json:"data"`
}

type mangaRelationship struct {
	Type       string `json:"type"`
	Attributes struct {
		FileName string `json:"fileName"`
	} `json:"attributes"`
}

type mangaFeedResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Chapter    string `json:"chapter"`
			PublishAt  string `json:"publishAt"`
			ReadableAt string `json:"readableAt"`
			CreatedAt  string `json:"createdAt"`
		} `json:"attributes"`
	} `json:"data"`
}
