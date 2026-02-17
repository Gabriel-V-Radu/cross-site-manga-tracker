package mangaplus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
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

var chapterNumberPattern = regexp.MustCompile(`\d+(?:\.\d+)?`)

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
			latestChapter, latestReleaseAt, _ := c.fetchLatestChapterByTitleID(ctx, title.TitleID)
			return &connectors.MangaResult{
				SourceKey:     c.Key(),
				SourceItemID:  title.TitleID,
				Title:         title.Name,
				URL:           "https://mangaplus.shueisha.co.jp/titles/" + title.TitleID,
				CoverImageURL: strings.TrimSpace(title.PortraitImageURL),
				LatestChapter: latestChapter,
				LastUpdatedAt: latestReleaseAt,
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
				CoverImageURL: strings.TrimSpace(title.PortraitImageURL),
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return len(results[i].Title) < len(results[j].Title)
	})

	if len(results) > limit {
		results = results[:limit]
	}

	for index := range results {
		latestChapter, latestReleaseAt, err := c.fetchLatestChapterByTitleID(ctx, results[index].SourceItemID)
		if err == nil {
			results[index].LatestChapter = latestChapter
			results[index].LastUpdatedAt = latestReleaseAt
		}
	}

	return results, nil
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
		return "", fmt.Errorf("url does not belong to mangaplus")
	}

	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) < 2 || segments[0] != "titles" {
		return "", fmt.Errorf("mangaplus url must match /titles/{id}")
	}

	titleID := strings.TrimSpace(segments[1])
	if _, err := strconv.Atoi(titleID); err != nil {
		return "", fmt.Errorf("invalid mangaplus title id")
	}

	values := url.Values{}
	values.Set("title_id", titleID)
	values.Set("format", "json")

	detailURL := c.apiBaseURL + "/title_detailV3?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, detailURL, nil)
	if err != nil {
		return "", fmt.Errorf("create title detail request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request title detail: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("title detail returned status %d", res.StatusCode)
	}

	var payload mangaPlusTitleDetailResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode title detail response: %w", err)
	}

	for _, group := range payload.Success.TitleDetailView.ChapterListGroup {
		for _, chapterItem := range group.FirstChapterList {
			if !chapterMatchesValue(chapterItem.Name, chapter) || chapterItem.ChapterID <= 0 {
				continue
			}
			return "https://mangaplus.shueisha.co.jp/viewer/" + strconv.Itoa(chapterItem.ChapterID), nil
		}
		for _, chapterItem := range group.MidChapterList {
			if !chapterMatchesValue(chapterItem.Name, chapter) || chapterItem.ChapterID <= 0 {
				continue
			}
			return "https://mangaplus.shueisha.co.jp/viewer/" + strconv.Itoa(chapterItem.ChapterID), nil
		}
		for _, chapterItem := range group.LastChapterList {
			if !chapterMatchesValue(chapterItem.Name, chapter) || chapterItem.ChapterID <= 0 {
				continue
			}
			return "https://mangaplus.shueisha.co.jp/viewer/" + strconv.Itoa(chapterItem.ChapterID), nil
		}
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

type mangaPlusTitle struct {
	TitleID          string
	Name             string
	PortraitImageURL string
}

func parseChapterValue(raw string) *float64 {
	match := chapterNumberPattern.FindString(strings.TrimSpace(raw))
	if match == "" {
		return nil
	}

	parsed, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return nil
	}

	return &parsed
}

func updateMaxChapter(current *float64, candidate *float64) *float64 {
	if candidate == nil {
		return current
	}
	if current == nil || *candidate > *current {
		return candidate
	}
	return current
}

func (c *Connector) fetchLatestChapterByTitleID(ctx context.Context, titleID string) (*float64, *time.Time, error) {
	if strings.TrimSpace(titleID) == "" {
		return nil, nil, nil
	}

	values := url.Values{}
	values.Set("title_id", titleID)
	values.Set("format", "json")

	detailURL := c.apiBaseURL + "/title_detailV3?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, detailURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create title detail request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request title detail: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("title detail returned status %d", res.StatusCode)
	}

	var payload mangaPlusTitleDetailResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, nil, fmt.Errorf("decode title detail response: %w", err)
	}

	var latest *float64
	var latestReleaseAt *time.Time
	for _, group := range payload.Success.TitleDetailView.ChapterListGroup {
		latest, latestReleaseAt = updateMaxChapterWithRelease(latest, latestReleaseAt, parseChapterValue(group.ChapterNumbers), parseUnixTime(group.StartTime))
		for _, chapter := range group.FirstChapterList {
			latest, latestReleaseAt = updateMaxChapterWithRelease(latest, latestReleaseAt, parseChapterValue(chapter.Name), parseUnixTime(chapter.StartTime))
		}
		for _, chapter := range group.MidChapterList {
			latest, latestReleaseAt = updateMaxChapterWithRelease(latest, latestReleaseAt, parseChapterValue(chapter.Name), parseUnixTime(chapter.StartTime))
		}
		for _, chapter := range group.LastChapterList {
			latest, latestReleaseAt = updateMaxChapterWithRelease(latest, latestReleaseAt, parseChapterValue(chapter.Name), parseUnixTime(chapter.StartTime))
		}
	}

	return latest, latestReleaseAt, nil
}

func updateMaxChapterWithRelease(currentChapter *float64, currentRelease *time.Time, candidateChapter *float64, candidateRelease *time.Time) (*float64, *time.Time) {
	if candidateChapter == nil {
		return currentChapter, currentRelease
	}
	if currentChapter == nil || *candidateChapter > *currentChapter {
		return candidateChapter, candidateRelease
	}
	if currentChapter != nil && *candidateChapter == *currentChapter && currentRelease == nil && candidateRelease != nil {
		return currentChapter, candidateRelease
	}
	return currentChapter, currentRelease
}

func parseUnixTime(raw int64) *time.Time {
	if raw <= 0 {
		return nil
	}
	if raw > 1_000_000_000_000 {
		raw = raw / 1000
	}
	parsed := time.Unix(raw, 0).UTC()
	return &parsed
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

		titles := payload.collectTitles()
		if len(titles) == 0 {
			lastErr = fmt.Errorf("empty title list")
			continue
		}

		items := make([]mangaPlusTitle, 0, len(titles))
		for _, item := range titles {
			if item.TitleID == 0 || strings.TrimSpace(item.Name) == "" {
				continue
			}
			items = append(items, mangaPlusTitle{
				TitleID:          strconv.Itoa(item.TitleID),
				Name:             strings.TrimSpace(item.Name),
				PortraitImageURL: strings.TrimSpace(item.PortraitImageURL),
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
				TitleID          int    `json:"titleId"`
				Name             string `json:"name"`
				PortraitImageURL string `json:"portraitImageUrl"`
			} `json:"titles"`
		} `json:"allTitlesView"`
		AllTitlesViewV2 struct {
			AllTitlesGroup []struct {
				Titles []struct {
					TitleID          int    `json:"titleId"`
					Name             string `json:"name"`
					PortraitImageURL string `json:"portraitImageUrl"`
				} `json:"titles"`
			} `json:"AllTitlesGroup"`
			AllTitlesGroupLower []struct {
				Titles []struct {
					TitleID          int    `json:"titleId"`
					Name             string `json:"name"`
					PortraitImageURL string `json:"portraitImageUrl"`
				} `json:"titles"`
			} `json:"allTitlesGroup"`
		} `json:"allTitlesViewV2"`
	} `json:"success"`
}

func (r mangaPlusTitleListResponse) collectTitles() []struct {
	TitleID          int
	Name             string
	PortraitImageURL string
} {
	titles := make([]struct {
		TitleID          int
		Name             string
		PortraitImageURL string
	}, 0)

	for _, item := range r.Success.AllTitlesView.Titles {
		titles = append(titles, struct {
			TitleID          int
			Name             string
			PortraitImageURL string
		}{TitleID: item.TitleID, Name: item.Name, PortraitImageURL: item.PortraitImageURL})
	}

	for _, group := range r.Success.AllTitlesViewV2.AllTitlesGroup {
		for _, item := range group.Titles {
			titles = append(titles, struct {
				TitleID          int
				Name             string
				PortraitImageURL string
			}{TitleID: item.TitleID, Name: item.Name, PortraitImageURL: item.PortraitImageURL})
		}
	}

	for _, group := range r.Success.AllTitlesViewV2.AllTitlesGroupLower {
		for _, item := range group.Titles {
			titles = append(titles, struct {
				TitleID          int
				Name             string
				PortraitImageURL string
			}{TitleID: item.TitleID, Name: item.Name, PortraitImageURL: item.PortraitImageURL})
		}
	}

	return titles
}

type mangaPlusTitleDetailResponse struct {
	Success struct {
		TitleDetailView struct {
			ChapterListGroup []struct {
				ChapterNumbers   string `json:"chapterNumbers"`
				StartTime        int64  `json:"startTime"`
				FirstChapterList []struct {
					ChapterID int    `json:"chapterId"`
					Name      string `json:"name"`
					StartTime int64  `json:"startTime"`
				} `json:"firstChapterList"`
				MidChapterList []struct {
					ChapterID int    `json:"chapterId"`
					Name      string `json:"name"`
					StartTime int64  `json:"startTime"`
				} `json:"midChapterList"`
				LastChapterList []struct {
					ChapterID int    `json:"chapterId"`
					Name      string `json:"name"`
					StartTime int64  `json:"startTime"`
				} `json:"lastChapterList"`
			} `json:"chapterListGroup"`
		} `json:"titleDetailView"`
	} `json:"success"`
}

func chapterMatchesValue(raw string, chapter float64) bool {
	parsed := parseChapterValue(raw)
	if parsed == nil {
		return false
	}
	return math.Abs(*parsed-chapter) <= 1e-9
}
