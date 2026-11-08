package freewebnovel

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const (
	canonicalHost    = "freewebnovel.com"
	canonicalBaseURL = "https://" + canonicalHost
)

// site is the connector's identity: the one list of hostnames it claims, and
// the canonical origin every returned or stored URL is built on (deliberately
// the production host rather than baseURL, which is a test server in tests,
// while the URLs handed back are stored in trackers and opened by the reader's
// browser). Only freewebnovel.com has ever been read here — other sites mirror
// the same catalogue, but a mirror belongs in this list only once its URLs are
// confirmed to follow the /novel/{slug}/chapter-{N} scheme the connector
// parses and rebuilds. FreeWebNovel is a readable novel site in the default
// tier.
var site = connectors.Site{
	SiteKey:   "freewebnovel",
	SiteName:  "FreeWebNovel",
	SiteHosts: []string{canonicalHost},
	Home:      canonicalBaseURL,
	Rank:      connectors.ReaderRankDefault,
}

var (
	searchRowSplitPattern    = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*\bli-row\b[^"']*["'][^>]*>`)
	searchTitleAnchorPattern = regexp.MustCompile(`(?is)<h3[^>]*class=["'][^"']*\btit\b[^"']*["'][^>]*>\s*<a[^>]+href=["']/novel/([^"'/?#]+)["'][^>]*>(.*?)</a>`)
	searchImgSrcPattern      = regexp.MustCompile(`(?is)<img[^>]+src=["']([^"']+)["'][^>]*>`)
	chapterHrefPattern       = regexp.MustCompile(`(?is)/novel/[^"'/]+/chapter-([0-9]+)`)
	novelLinkPattern         = regexp.MustCompile(`(?i)href=["'](?:https?://[^"'/]+)?/novel/[^"'/?#]+`)

	ogTitlePattern       = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]*content="([^"]*)"`)
	ogImagePattern       = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:image["'][^>]*content="([^"]*)"`)
	novelNamePattern     = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:novel_name["'][^>]*content="([^"]*)"`)
	updateTimePattern    = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:update_time["'][^>]*content="([^"]*)"`)
	latestChapterURLPatt = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:lastest_chapter_url["'][^>]*content="([^"]*)"`)
	titleHeadingPattern  = regexp.MustCompile(`(?is)<h1[^>]*class=["'][^"']*\btit\b[^"']*["'][^>]*>(.*?)</h1>`)
	alternativeNamesPatt = regexp.MustCompile(`(?is)title=["']Alternative names["'][^>]*>.*?<div[^>]*class=["'][^"']*\bright\b[^"']*["'][^>]*>\s*<span[^>]*class=["'][^"']*\bs1\b[^"']*["'][^>]*>(.*?)</span>`)

	// The series metadata a novel page carries (verified live 2026-11-08). The
	// og:novel:* values are as clean as the title's; the synopsis is whole only
	// in the page body, under the SUMMARY heading — og:description and the
	// JSON-LD description are both cut off mid-word.
	novelGenrePattern    = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:genre["'][^>]*content="([^"]*)"`)
	novelStatusPattern   = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:status["'][^>]*content="([^"]*)"`)
	novelAuthorPattern   = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:novel:author["'][^>]*content="([^"]*)"`)
	ogDescriptionPattern = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:description["'][^>]*content="([^"]*)"`)
	summaryPattern       = regexp.MustCompile(`(?is)<h4[^>]*class=["'][^"']*\babstract\b[^"']*["'][^>]*>.*?</h4>\s*<div[^>]*class=["'][^"']*\btxt\b[^"']*["'][^>]*>\s*<div[^>]*class=["'][^"']*\binner\b[^"']*["'][^>]*>(.*?)</div>`)
	jsonLDPattern        = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	// descriptionLeadPattern is the boilerplate og:description opens with:
	// "Read {title} by {author} online for free. {synopsis…}".
	descriptionLeadPattern = regexp.MustCompile(`(?is)^\s*Read\s.+?\sonline for free\.\s*`)
)

// Connector reads freewebnovel.com by scraping its pages. The embedded Site
// supplies Key, Name, Kind, the SiteInfo methods and the URL helpers; baseURL
// is where requests go (the live site, or a test server).
type Connector struct {
	connectors.Site
	baseURL    string
	httpClient *http.Client

	warmMu sync.Mutex
	warmed bool
}

type searchEntry struct {
	Slug          string
	Title         string
	CoverImage    string
	LatestChapter *float64
}

func NewConnector() *Connector {
	return &Connector{
		Site:       site,
		baseURL:    canonicalBaseURL,
		httpClient: newChromeHTTPClient(connectors.MinClientTimeout),
	}
}

// NewConnectorWithOptions points the connector at another base URL (a test
// server), optionally claiming other hosts. A nil client gets this connector's
// own Chrome-fingerprint client (see newChromeHTTPClient), which is already
// paced by the shared throttle; tests that want to stay unpaced pass their own.
func NewConnectorWithOptions(baseURL string, allowedHost []string, client *http.Client) *Connector {
	if client == nil {
		client = newChromeHTTPClient(connectors.MinClientTimeout)
	}
	if client.Jar == nil {
		if jar, err := cookiejar.New(nil); err == nil {
			client.Jar = jar
		}
	}
	identity := site
	if len(allowedHost) > 0 {
		identity.SiteHosts = allowedHost
	}
	return &Connector{
		Site:       identity,
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: client,
	}
}

// newChromeHTTPClient builds an HTTP client whose TLS handshake mimics Google
// Chrome (via utls). freewebnovel.com sits behind Cloudflare, which fingerprints
// the TLS ClientHello (JA3) and blocks Go's default net/http signature from
// lower-reputation IPs even when the headers look like a browser. Presenting a
// real Chrome fingerprint clears that check. ALPN is pinned to HTTP/1.1 so the
// negotiated protocol matches net/http's HTTP/1.1 transport (pinning ALPN does
// not change the JA3, which keys on extension types, not their values).
//
// The timeout has to be the shared throttle's floor, not a per-request figure:
// the transport paces requests to the host, the wait for a slot counts toward
// Client.Timeout, and the 12s this used to be cut down any request queued more
// than a dozen deep behind a burst — while the slot it had claimed stayed
// claimed. Per-request deadlines are the callers' contexts.
func newChromeHTTPClient(timeout time.Duration) *http.Client {
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		DialTLSContext:        dialChromeTLS,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   12 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Timeout: timeout, Jar: jar, Transport: connectors.ThrottleTransport(transport)}
}

func dialChromeTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	rawConn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	uconn := utls.UClient(rawConn, &utls.Config{ServerName: host}, utls.HelloCustom)

	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("build chrome tls spec: %w", err)
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	if err := uconn.ApplyPreset(&spec); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("apply chrome tls spec: %w", err)
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("chrome tls handshake: %w", err)
	}

	return uconn, nil
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	body, err := c.fetchPage(ctx, c.baseURL+"/home", "")
	if err != nil {
		return err
	}
	c.warmMu.Lock()
	c.warmed = true
	c.warmMu.Unlock()
	// A 200 only proves the host answers (Cloudflare can serve its own page
	// with one). The home page links novels by their /novel/{slug} path, the
	// scheme every resolve and chapter link here is built on, so a page with
	// no such link means the site is not what the connector reads.
	if !novelLinkPattern.MatchString(body) {
		return fmt.Errorf("freewebnovel home page links no novel")
	}
	return nil
}

func (c *Connector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	slug, err := c.parseNovelURL(rawURL)
	if err != nil {
		return nil, err
	}

	return c.resolveBySlug(ctx, slug)
}

func (c *Connector) SearchByTitle(ctx context.Context, title string, limit int) ([]connectors.MangaResult, error) {
	query, err := connectors.PrepareSearch(title, limit)
	if err != nil {
		return nil, err
	}

	body, err := c.fetchPageResilient(ctx, c.baseURL+"/search?keyword="+url.QueryEscape(query.Raw), c.baseURL+"/")
	if err != nil {
		return nil, fmt.Errorf("fetch freewebnovel search page: %w", err)
	}

	entries := parseSearchEntries(body)
	if len(entries) == 0 {
		return []connectors.MangaResult{}, nil
	}

	results := make([]connectors.MangaResult, 0, min(query.Limit, len(entries)))
	for _, entry := range entries {
		if !query.Matches(entry.Title, entry.Slug) {
			continue
		}

		result := connectors.MangaResult{
			SourceKey:     c.Key(),
			SourceItemID:  entry.Slug,
			Title:         entry.Title,
			URL:           c.novelURL(entry.Slug),
			CoverImageURL: c.AbsoluteURL(entry.CoverImage),
		}

		if entry.LatestChapter != nil {
			latest := *entry.LatestChapter
			result.LatestChapter = &latest
		}

		results = append(results, result)
		if len(results) >= query.Limit {
			break
		}
	}

	return results, nil
}

func (c *Connector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if !connectors.ValidChapterWithin(chapter, connectors.MaxPlausibleNovelChapter) {
		// Wrapped so the reading chain reads this as "this site does not carry
		// that chapter" and cedes the turn, rather than as "the site could not
		// be asked", which would leave the reader on the bare series URL.
		return "", fmt.Errorf("invalid chapter %s: %w", connectors.FormatChapter(chapter), connectors.ErrChapterNotFound)
	}

	slug, err := c.parseNovelURL(rawURL)
	if err != nil {
		return "", err
	}

	// FreeWebNovel chapter URLs are a deterministic sequential index:
	// /novel/{slug}/chapter-{N}. The tracked chapter number is that same
	// index (taken from og:novel:lastest_chapter_url), so we can build the
	// URL directly without fetching a chapter list.
	return c.novelURL(slug) + "/chapter-" + connectors.FormatChapter(chapter), nil
}

// parseNovelURL checks the URL is this site's and reads the novel slug out of
// its /novel/{slug} path; chapter pages (/novel/{slug}/chapter-{N}) share it.
func (c *Connector) parseNovelURL(rawURL string) (string, error) {
	parsed, err := c.ParseOwnedURL(rawURL)
	if err != nil {
		return "", err
	}

	segments := connectors.PathSegments(parsed)
	if len(segments) < 2 || segments[0] != "novel" {
		return "", fmt.Errorf("freewebnovel url must match /novel/{id}")
	}

	slug := strings.TrimSpace(segments[1])
	if slug == "" {
		return "", fmt.Errorf("freewebnovel url must match /novel/{id}")
	}
	return slug, nil
}

func (c *Connector) resolveBySlug(ctx context.Context, slug string) (*connectors.MangaResult, error) {
	body, err := c.fetchPageResilient(ctx, c.baseURL+"/novel/"+slug, c.baseURL+"/")
	if err != nil {
		return nil, fmt.Errorf("fetch novel page: %w", err)
	}

	title := extractTitle(body, slug)
	relatedTitles := extractRelatedTitles(body, title)

	coverImageURL := c.AbsoluteURL(strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(ogImagePattern, body))))

	latestChapter := parseLatestChapterFromURL(connectors.FirstSubmatch(latestChapterURLPatt, body))
	// Novel pages carry og:novel:lastest_chapter_url, so a page that loads
	// without one is a markup change or not a novel page at all. Taken
	// as a success it would stamp the tracker as checked with no log line and
	// no turn for its other sources.
	if latestChapter == nil {
		return nil, fmt.Errorf("novel %s: %w", slug, connectors.ErrNoChaptersListed)
	}
	lastUpdatedAt := parseUpdateTime(connectors.FirstSubmatch(updateTimePattern, body))

	return &connectors.MangaResult{
		SourceKey:     c.Key(),
		SourceItemID:  slug,
		Title:         title,
		RelatedTitles: relatedTitles,
		URL:           c.novelURL(slug),
		CoverImageURL: coverImageURL,
		LatestChapter: latestChapter,
		LastUpdatedAt: lastUpdatedAt,
		Metadata:      novelMetadata(body),
	}, nil
}

// novelMetadata reads what a novel page says about the work. Everything on
// this site is a novel, so the type is fixed and the result is never empty;
// every other field is optional and a page missing it simply leaves it zero.
func novelMetadata(body string) *connectors.SeriesMetadata {
	metadata := &connectors.SeriesMetadata{
		Type:        connectors.SeriesTypeNovel,
		Status:      connectors.NormalizeSeriesStatus(html.UnescapeString(connectors.FirstSubmatch(novelStatusPattern, body))),
		Genres:      connectors.SplitLabels(connectors.FirstSubmatch(novelGenrePattern, body)),
		Authors:     connectors.SplitLabels(connectors.FirstSubmatch(novelAuthorPattern, body)),
		Description: novelDescription(body),
	}
	metadata.Rating, metadata.RatingVotes = novelRating(body)
	return metadata
}

// novelDescription prefers the whole synopsis from the page body. The
// og:description fallback is cut off ("…where...") and opens with a stock
// sentence naming the title and author, which is stripped; one that does not
// open that way is some other text and is not used.
func novelDescription(body string) string {
	if summary := connectors.PlainDescription(connectors.FirstSubmatch(summaryPattern, body)); summary != "" {
		return summary
	}
	raw := connectors.FirstSubmatch(ogDescriptionPattern, body)
	lead := descriptionLeadPattern.FindStringIndex(raw)
	if lead == nil {
		return ""
	}
	return connectors.PlainDescription(raw[lead[1]:])
}

// jsonLDBook is the part of the page's schema.org Book block the connector
// reads. The numbers are json.Number because schema.org allows them as text
// as well ("4.7"), and json.Number decodes either.
type jsonLDBook struct {
	AggregateRating *struct {
		RatingValue json.Number `json:"ratingValue"`
		BestRating  json.Number `json:"bestRating"`
		RatingCount json.Number `json:"ratingCount"`
	} `json:"aggregateRating"`
}

// novelRating reads the readers' score from the page's JSON-LD (4.7 out of 5
// becomes 9.4) and how many votes it averages. A block that does not decode,
// or a novel nobody has rated (ratingValue 0), is no rating.
func novelRating(body string) (*float64, int) {
	for _, match := range jsonLDPattern.FindAllStringSubmatch(body, -1) {
		var book jsonLDBook
		if err := json.Unmarshal([]byte(strings.TrimSpace(match[1])), &book); err != nil || book.AggregateRating == nil {
			continue
		}
		aggregate := book.AggregateRating
		// schema.org's default scale is 1 to 5, the one the site uses.
		outOf := 5.0
		if best, err := aggregate.BestRating.Float64(); err == nil && best > 0 {
			outOf = best
		}
		rating := connectors.ParseRating(aggregate.RatingValue.String(), outOf)
		if rating == nil {
			return nil, 0
		}
		votes := 0
		if count, err := aggregate.RatingCount.Float64(); err == nil && count > 0 {
			votes = int(count)
		}
		return rating, votes
	}
	return nil, 0
}

func parseSearchEntries(body string) []searchEntry {
	// Each search result is wrapped in <div class="li-row">. The block is
	// div-based (no clean closing delimiter), so slice the body on the row
	// markers and parse the first result within each slice.
	markers := searchRowSplitPattern.FindAllStringIndex(body, -1)
	if len(markers) == 0 {
		return nil
	}

	entries := make([]searchEntry, 0, len(markers))
	seen := make(map[string]struct{}, len(markers))
	for index, marker := range markers {
		start := marker[1]
		end := len(body)
		if index+1 < len(markers) {
			end = markers[index+1][0]
		}
		block := body[start:end]

		anchor := searchTitleAnchorPattern.FindStringSubmatch(block)
		if len(anchor) < 3 {
			continue
		}

		slug := strings.TrimSpace(anchor[1])
		if slug == "" {
			continue
		}
		if _, exists := seen[slug]; exists {
			continue
		}

		title := connectors.CleanText(anchor[2])
		if title == "" {
			title = titleFromSlug(slug)
		}

		coverImageURL := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(searchImgSrcPattern, block)))

		var latestChapter *float64
		if chapterRaw := connectors.FirstSubmatch(chapterHrefPattern, block); chapterRaw != "" {
			latestChapter = parseChapterNumber(chapterRaw)
		}

		seen[slug] = struct{}{}
		entries = append(entries, searchEntry{
			Slug:          slug,
			Title:         title,
			CoverImage:    coverImageURL,
			LatestChapter: latestChapter,
		})
	}

	return entries
}

func extractTitle(body string, slug string) string {
	title := strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(ogTitlePattern, body)))
	if title != "" {
		return title
	}

	title = strings.TrimSpace(html.UnescapeString(connectors.FirstSubmatch(novelNamePattern, body)))
	if title != "" {
		return title
	}

	title = connectors.CleanText(connectors.FirstSubmatch(titleHeadingPattern, body))
	if title != "" {
		return title
	}

	return titleFromSlug(slug)
}

// extractRelatedTitles reads the page's "Alternative names" block (a
// comma-separated span) and the generic related-title markup, then reduces
// them to the Latin-alphabet names worth storing beside the primary title.
func extractRelatedTitles(body string, primaryTitle string) []string {
	candidates := make([]string, 0, 16)

	altRaw := connectors.CleanText(connectors.FirstSubmatch(alternativeNamesPatt, body))
	if altRaw != "" {
		for _, part := range strings.Split(altRaw, ",") {
			candidate := strings.TrimSpace(part)
			if candidate == "" {
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	candidates = append(candidates, searchutil.ExtractRelatedTitles(body)...)
	return searchutil.RelatedTitles(primaryTitle, candidates)
}

// warm performs a best-effort homepage fetch so the shared cookie jar picks up
// any Cloudflare clearance cookie before hitting the search/novel endpoints,
// which a bare (cookie-less) request is more likely to have challenged with a
// 403. It only marks success so a transient failure is retried next time.
func (c *Connector) warm(ctx context.Context) {
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	if c.warmed {
		return
	}

	warmCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := c.fetchPage(warmCtx, c.baseURL+"/home", ""); err == nil {
		c.warmed = true
	}
}

// fetchPageResilient warms the session, fetches, and — if the request fails
// (e.g. Cloudflare challenged a cold request or the clearance cookie expired) —
// re-warms with a fresh homepage hit and retries once.
func (c *Connector) fetchPageResilient(ctx context.Context, endpoint string, referer string) (string, error) {
	c.warm(ctx)
	body, err := c.fetchPage(ctx, endpoint, referer)
	if err == nil {
		return body, nil
	}
	// A 404 is the site answering, not challenging us: the novel was removed
	// or the slug is wrong, and no clearance cookie changes that. Re-warming
	// would spend two more requests to be told the same thing.
	if connectors.IsNotFound(err) {
		return "", err
	}

	c.warmMu.Lock()
	c.warmed = false
	c.warmMu.Unlock()
	c.warm(ctx)

	return c.fetchPage(ctx, endpoint, referer)
}

func (c *Connector) fetchPage(ctx context.Context, endpoint string, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// Present as a real Chrome navigation. freewebnovel.com sits behind
	// Cloudflare, which challenges requests that don't look browser-like.
	// These are set before the shared fetch helper runs: it fills in only the
	// headers a connector left blank, so this site-specific set survives.
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Ch-Ua", connectors.BrowserSecChUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	if referer != "" {
		req.Header.Set("Referer", referer)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	} else {
		req.Header.Set("Sec-Fetch-Site", "none")
	}

	rawBody, _, err := connectors.FetchBytes(c.httpClient, req, 0)
	if err != nil {
		return "", err
	}

	return string(rawBody), nil
}

func parseLatestChapterFromURL(raw string) *float64 {
	chapterRaw := connectors.FirstSubmatch(chapterHrefPattern, strings.TrimSpace(html.UnescapeString(raw)))
	if chapterRaw == "" {
		return nil
	}
	return parseChapterNumber(chapterRaw)
}

// parseChapterNumber reads a chapter index out of a /chapter-{N} href. The
// plausibility guard keeps a stray id or year picked up by the pattern from
// inflating a tracker to a number the site will never reach. It uses the novel
// bound, not the comic one: the serials here genuinely run into the thousands,
// and rejecting a real chapter would freeze the tracker instead of protecting it.
func parseChapterNumber(raw string) *float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !connectors.ValidChapterWithin(value, connectors.MaxPlausibleNovelChapter) {
		return nil
	}
	return &value
}

// siteTimeZone is the zone og:novel:update_time is rendered in. The site emits
// wall-clock times in UTC+8: cross-checking live pages, the meta value is
// consistently 8 hours ahead of the "[ Updated N hours ago ]" text shown on the
// same page. Parsing it as UTC pushed release dates up to 8h into the future,
// which the dashboard clamps to "just now".
var siteTimeZone = time.FixedZone("UTC+8", 8*60*60)

func parseUpdateTime(raw string) *time.Time {
	normalized := strings.TrimSpace(html.UnescapeString(raw))
	if normalized == "" {
		return nil
	}

	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, normalized, siteTimeZone)
		if err != nil {
			continue
		}
		utc := parsed.UTC()
		return &utc
	}

	return nil
}

// titleFromSlug is the display-title fallback for a page that carries no
// usable title. connectors.PrettifySlug leaves a slug made of nothing but
// separators empty, while every tracker row needs something to show, so the
// placeholder this connector has always used is kept here.
func titleFromSlug(slug string) string {
	if pretty := connectors.PrettifySlug(slug); pretty != "" {
		return pretty
	}
	return "Untitled"
}

// novelURL is the stored, canonical address of a novel: always on the real
// site, never on the base URL requests go to.
func (c *Connector) novelURL(slug string) string {
	return c.HomeURL() + "/novel/" + slug
}
