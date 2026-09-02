// Package mangadex reads MangaDex (mangadex.org) through its open, unsigned
// JSON API at api.mangadex.org.
//
// Endpoints used:
//   - GET /ping                                → liveness, answers plain text
//   - GET /manga/{id}?includes[]=cover_art     → titles, alt titles, cover,
//     and attributes.lastChapter
//   - GET /manga?title=&limit=                 → search
//   - GET /manga/{id}/feed?translatedLanguage[]=en&order[chapter]=desc
//     → the English chapters, the only place release dates come from, and the
//     chapter ids reader URLs are built from.
//
// Every feed read is English-scoped on purpose: MangaDex numbers chapters per
// language, so an unscoped feed inflates the count with raw uploads.
package mangadex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const (
	canonicalSiteURL = "https://mangadex.org"
	canonicalAPIURL  = "https://api.mangadex.org"
	// coverCDNURL is a separate origin from the site on purpose: covers are
	// served from the upload CDN, never from mangadex.org itself.
	coverCDNURL = "https://uploads.mangadex.org"
)

// site is the connector's identity. One host covers the whole site: the API
// (api.mangadex.org) and the cover CDN (uploads.mangadex.org) are subdomains,
// which connectors.HostAllowed matches, and mangadex.org has never moved
// domain. Home is the production site rather than the API host: the API is
// where requests go (a test server in tests) and never serves pages, while the
// URLs handed back are stored in trackers and opened by the reader's browser.
// MangaDex sits in the default reading tier — its own reader is fine, but
// scanlations reach it after the origin sites and it freezes on licensed
// series.
var site = connectors.Site{
	SiteKey:   "mangadex",
	SiteName:  "MangaDex",
	SiteHosts: []string{"mangadex.org"},
	Home:      canonicalSiteURL,
	Rank:      connectors.ReaderRankDefault,
}

var titleIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

// The two feed reads ask for different page sizes: resolving a chapter URL has
// to find one specific number anywhere in the series, while tracking only needs
// the newest ones.
const (
	chapterLookupFeedLimit = 500
	latestChapterFeedLimit = 100
)

// Connector reads MangaDex through its JSON API. The embedded Site supplies
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
		apiBaseURL: canonicalAPIURL,
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
		apiBaseURL: strings.TrimRight(apiBaseURL, "/"),
		httpClient: client,
	}
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/ping", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// /ping answers "pong" in plain text, so the body is read only to be
	// discarded; the status is the whole verdict.
	if _, _, err := connectors.FetchBytes(c.httpClient, req, 0); err != nil {
		return fmt.Errorf("request ping: %w", err)
	}

	return nil
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	trimmed := strings.TrimSpace(rawURL)
	titleID, err := c.parseTitleURL(trimmed)
	if err != nil {
		return nil, err
	}

	values := url.Values{}
	values.Add("includes[]", "cover_art")

	var payload mangaByIDResponse
	if err := connectors.FetchJSON(ctx, c.httpClient, c.apiBaseURL+"/manga/"+titleID+"?"+values.Encode(), &payload); err != nil {
		return nil, fmt.Errorf("fetch mangadex manga: %w", err)
	}

	title, relatedTitles := pickTitles(payload.Data.Attributes.Title, payload.Data.Attributes.AltTitles)

	// lastChapter outlives the English feed: a licensed series whose chapters
	// were pulled, and a oneshot whose only chapter has a null number, keep
	// serving a number here while the feed — the sole source of release dates —
	// answers empty. Such a series legitimately comes back with a chapter and no
	// LastUpdatedAt, which is what trackers.latest_chapter_seen_at exists to
	// cover; attributes.updatedAt is not a substitute, it tracks metadata edits.
	latestChapter := parseChapterNumber(payload.Data.Attributes.LastChapter)
	// A feed that could not be read is a failed resolve, not an empty one: a 429
	// or a timeout here used to come back as a result with no chapter and no
	// date, which the poller records as a successful check — no fallback to the
	// tracker's other sources, no warning, the cycle quietly lost. An empty feed
	// (a licensed series, a oneshot) is still a legitimate answer; that is the
	// (nil, nil, nil) return below, not an error.
	feedLatestChapter, latestReleaseAt, err := c.fetchLatestChapterFromFeed(ctx, titleID)
	if err != nil {
		return nil, err
	}
	if latestChapter == nil {
		latestChapter = feedLatestChapter
	}

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  payload.Data.ID,
		Title:         title,
		RelatedTitles: relatedTitles,
		// The caller's own URL is echoed back rather than rebuilt from the id:
		// MangaDex title URLs carry an optional trailing slug (/title/{id}/{slug})
		// that users' stored links keep, and dropping it would rewrite every
		// linked source on the next resolve.
		URL:           trimmed,
		CoverImageURL: pickCoverImageURL(payload.Data.ID, payload.Data.Relationships),
		LatestChapter: latestChapter,
		LastUpdatedAt: latestReleaseAt,
	}, nil
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	// The API is asked for a wider page than the caller wants because results
	// are filtered against the query afterwards: MangaDex matches loosely, so a
	// 1:1 page comes back short. 50 is the API's own per-page ceiling.
	requestLimit := min(query.Limit*4, 50)

	values := url.Values{}
	values.Set("title", query.Raw)
	values.Set("limit", strconv.Itoa(requestLimit))
	values.Add("includes[]", "cover_art")

	var payload mangaSearchResponse
	if err := connectors.FetchJSON(ctx, c.httpClient, c.apiBaseURL+"/manga?"+values.Encode(), &payload); err != nil {
		return nil, fmt.Errorf("search mangadex titles: %w", err)
	}

	items := make([]connectors.MangaResult, 0, min(query.Limit, len(payload.Data)))
	for _, item := range payload.Data {
		bestTitle, englishRelatedTitles := pickTitles(item.Attributes.Title, item.Attributes.AltTitles)
		if !query.Matches(append([]string{bestTitle}, englishRelatedTitles...)...) {
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
			URL:           c.HomeURL() + "/title/" + item.ID,
			CoverImageURL: pickCoverImageURL(item.ID, item.Relationships),
			LatestChapter: latestChapter,
		})

		if len(items) >= query.Limit {
			break
		}
	}

	return items, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapter(chapter) {
		return "", fmt.Errorf("invalid chapter")
	}

	titleID, err := c.parseTitleURL(rawURL)
	if err != nil {
		return "", err
	}

	var payload mangaFeedResponse
	if err := connectors.FetchJSON(ctx, c.httpClient, c.feedURL(titleID, chapterLookupFeedLimit), &payload); err != nil {
		return "", fmt.Errorf("fetch mangadex feed: %w", err)
	}

	for _, chapterItem := range payload.Data {
		parsedChapter := parseChapterNumber(chapterItem.Attributes.Chapter)
		if parsedChapter == nil {
			continue
		}
		if !connectors.SameChapter(*parsedChapter, chapter) {
			continue
		}

		chapterID := strings.TrimSpace(chapterItem.ID)
		if chapterID == "" {
			continue
		}

		return c.HomeURL() + "/chapter/" + chapterID, nil
	}

	return "", fmt.Errorf("chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
}

// parseTitleURL checks the URL is this site's and extracts the title id from
// its /title/{id} path (an optional trailing slug segment is ignored).
func (c *Connector) parseTitleURL(rawURL string) (string, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", err
	}

	segments := connectors.PathSegments(parsed)
	if len(segments) < 2 || segments[0] != "title" {
		return "", fmt.Errorf("mangadex url must match /title/{id}")
	}

	titleID := strings.TrimSpace(segments[1])
	if !titleIDPattern.MatchString(titleID) {
		return "", fmt.Errorf("invalid mangadex title id")
	}

	return titleID, nil
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

// pickTitles chooses the display title and the English alternates worth
// storing beside it, out of the record's title map and altTitles. A record with
// no usable primary title is named after its first English alternate, which is
// then no longer an alternate; one with nothing at all is "Untitled".
func pickTitles(titleMap map[string]string, altTitles []map[string]string) (string, []string) {
	candidates := make([]string, 0, len(titleMap)+(len(altTitles)*2))
	for _, value := range titleMap {
		candidates = append(candidates, value)
	}
	for _, altTitleMap := range altTitles {
		for _, value := range altTitleMap {
			candidates = append(candidates, value)
		}
	}

	title := pickBestTitle(titleMap)
	related := searchutil.RelatedTitles(title, candidates)
	if title == "" {
		if len(related) == 0 {
			return "Untitled", nil
		}
		title = related[0]
		related = searchutil.RelatedTitles(title, related)
	}
	return title, related
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
		return coverCDNURL + "/covers/" + mangaID + "/" + fileName + ".256.jpg"
	}

	return ""
}

func parseChapterNumber(raw string) *float64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || !connectors.ValidChapter(parsed) {
		return nil
	}

	return &parsed
}

func (c *Connector) fetchLatestChapterFromFeed(ctx context.Context, mangaID string) (*float64, *time.Time, error) {
	if strings.TrimSpace(mangaID) == "" {
		return nil, nil, nil
	}

	var payload mangaFeedResponse
	if err := connectors.FetchJSON(ctx, c.httpClient, c.feedURL(mangaID, latestChapterFeedLimit), &payload); err != nil {
		// A feed the API says does not exist is an empty feed, not an outage:
		// the record answered, there is simply nothing to read. Every other
		// failure — a rate limit, a timeout, a 5xx — is the site not answering
		// and is returned as such.
		if connectors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("fetch mangadex feed: %w", err)
	}

	var latest connectors.LatestReading
	for _, chapter := range payload.Data {
		parsed := parseChapterNumber(chapter.Attributes.Chapter)
		if parsed == nil {
			continue
		}
		latest.Add(*parsed, parseChapterTime(
			chapter.Attributes.PublishAt,
			chapter.Attributes.ReadableAt,
			chapter.Attributes.CreatedAt,
		))
	}

	latestChapter, latestReleaseAt := latest.Result()
	return latestChapter, latestReleaseAt, nil
}

// feedURL builds a chapter feed request. Both callers share one builder so the
// filters stay identical: English only, external-only chapters excluded because
// they have no MangaDex page to link to, and every content rating listed
// because the API's default set hides erotica and pornographic entries, which
// would silently report a stale latest chapter for those series.
func (c *Connector) feedURL(mangaID string, limit int) string {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(limit))
	values.Set("offset", "0")
	values.Set("order[chapter]", "desc")
	values.Set("includeExternalUrl", "0")
	values.Add("translatedLanguage[]", "en")
	values.Add("contentRating[]", "safe")
	values.Add("contentRating[]", "suggestive")
	values.Add("contentRating[]", "erotica")
	values.Add("contentRating[]", "pornographic")

	return c.apiBaseURL + "/manga/" + mangaID + "/feed?" + values.Encode()
}

// parseChapterTime takes the first timestamp MangaDex actually filled in:
// publishAt is the release, readableAt and createdAt are the fallbacks older
// entries carry.
func parseChapterTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed := connectors.ParseFirstTime(value, time.RFC3339); parsed != nil {
			return parsed
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
