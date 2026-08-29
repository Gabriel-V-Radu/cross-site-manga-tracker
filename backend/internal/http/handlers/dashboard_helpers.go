package handlers

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

var valueLabelReplacer = strings.NewReplacer("_", " ", "-", " ")

func parseTagNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		normalized := strings.ToLower(tag)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func parseTagNamesFromQuery(c *fiber.Ctx) []string {
	queryValues := c.Context().QueryArgs().PeekMulti("tags")
	if len(queryValues) == 0 {
		return parseTagNames(c.Query("tags"))
	}

	values := make([]string, 0, len(queryValues))
	for _, value := range queryValues {
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}

	if len(values) == 0 {
		return nil
	}

	return parseTagNames(strings.Join(values, ","))
}

func parseSourceIDs(raw string) []int64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		sourceID, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || sourceID <= 0 {
			continue
		}
		if _, exists := seen[sourceID]; exists {
			continue
		}
		seen[sourceID] = struct{}{}
		out = append(out, sourceID)
	}

	return out
}

func parseSourceIDsFromQuery(c *fiber.Ctx) []int64 {
	queryValues := c.Context().QueryArgs().PeekMulti("sites")
	if len(queryValues) == 0 {
		return parseSourceIDs(c.Query("sites"))
	}

	values := make([]string, 0, len(queryValues))
	for _, value := range queryValues {
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}

	if len(values) == 0 {
		return nil
	}

	return parseSourceIDs(strings.Join(values, ","))
}

func sourceIDFilterMap(sourceIDs []int64) map[int64]bool {
	ids := make(map[int64]bool, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if sourceID <= 0 {
			continue
		}
		ids[sourceID] = true
	}
	return ids
}

func toTrackerTagView(tags []models.CustomTag) []trackerTagView {
	items := make([]trackerTagView, 0, len(tags))
	for _, tag := range tags {
		items = append(items, trackerTagView{
			ID:       tag.ID,
			Name:     tag.Name,
			IconKey:  tag.IconKey,
			IconPath: tag.IconPath(),
		})
	}
	return items
}

func toTrackerTagIcons(tags []models.CustomTag) []trackerTagIconView {
	icons := make([]trackerTagIconView, 0, len(tags))
	for _, tag := range tags {
		iconPath := tag.IconPath()
		if iconPath == "" {
			continue
		}
		icons = append(icons, trackerTagIconView{TagName: tag.Name, IconPath: iconPath})
	}
	return icons
}

func sourceHomeURLForKey(sourceKey string) string {
	switch strings.ToLower(strings.TrimSpace(sourceKey)) {
	case "asuracomic":
		return "https://asurascans.com"
	case "flamecomics":
		return "https://flamecomics.xyz"
	case "mangadex":
		return "https://mangadex.org"
	case "mangafire":
		return "https://mangafire.to"
	case "mgeko":
		return "https://www.mgeko.cc"
	case "webtoons":
		return "https://www.webtoons.com"
	case "freewebnovel":
		return "https://freewebnovel.com"
	case "mangaupdates":
		return "https://www.mangaupdates.com"
	case "comick":
		return "https://comick.dev"
	case "mangahub":
		return "https://mangahub.io"
	default:
		return ""
	}
}

func prioritizeTrackerTags(tags []trackerTagView, maxVisible int) ([]trackerTagView, int) {
	if maxVisible <= 0 || len(tags) == 0 {
		return nil, len(tags)
	}

	withIcon := make([]trackerTagView, 0, len(tags))
	withoutIcon := make([]trackerTagView, 0, len(tags))
	for _, tag := range tags {
		if tag.IconPath != "" {
			withIcon = append(withIcon, tag)
			continue
		}
		withoutIcon = append(withoutIcon, tag)
	}

	ordered := make([]trackerTagView, 0, len(tags))
	ordered = append(ordered, withIcon...)
	ordered = append(ordered, withoutIcon...)

	if len(ordered) <= maxVisible {
		return ordered, 0
	}

	return ordered[:maxVisible], len(ordered) - maxVisible
}

func formatChapterLabel(chapter float64) string {
	return "Ch. " + strconv.FormatFloat(chapter, 'f', -1, 64)
}

func formatRatingLabel(rating float64) string {
	return strconv.FormatFloat(rating, 'f', 1, 64)
}

func chapterInputValue(chapter *float64) string {
	if chapter == nil {
		return ""
	}
	if math.IsNaN(*chapter) || math.IsInf(*chapter, 0) {
		return ""
	}
	return strconv.FormatFloat(*chapter, 'f', -1, 64)
}

func textInputValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func timeInputValue(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func relativeTime(value time.Time) string {
	now := time.Now().UTC()
	target := value.UTC()
	if target.After(now) {
		return "just now"
	}

	delta := now.Sub(target)
	if delta < time.Minute {
		return "just now"
	}
	if delta < time.Hour {
		minutes := int(delta / time.Minute)
		return fmt.Sprintf("%d min ago", minutes)
	}
	if delta < 24*time.Hour {
		hours := int(delta / time.Hour)
		return fmt.Sprintf("%d hours ago", hours)
	}
	if delta < 30*24*time.Hour {
		days := int(delta / (24 * time.Hour))
		return fmt.Sprintf("%d days ago", days)
	}
	if delta < 365*24*time.Hour {
		months := int(delta / (30 * 24 * time.Hour))
		return fmt.Sprintf("%d months ago", months)
	}
	years := int(delta / (365 * 24 * time.Hour))
	return fmt.Sprintf("%d years ago", years)
}

// trackerAlternatesForProfile loads a profile's alternate linked sources so a
// re-rendered card resolves its cover and chapter links the same way the list
// does. A load failure degrades to no fallback rather than failing the render:
// the card is still useful without cover art.
func (h *DashboardHandler) trackerAlternatesForProfile(profileID int64) map[int64][]repository.TrackerSourceRef {
	alternates, err := h.trackerRepo.ListAlternateSourcesByTracker(profileID)
	if err != nil {
		return nil
	}
	return alternates
}

// How long a failed lookup is remembered. These exist to stop a page of failing
// trackers from re-querying their sources every couple of minutes for as long as
// the page stays open: the sources this app reads are exactly the ones that
// respond to being hammered by putting up a bot challenge, so a failure is held
// long enough to be worth something. The short span still recovers from an
// outage within one sitting.
const (
	lookupRetryTTL       = 10 * time.Minute
	lookupUnreachableTTL = 30 * time.Minute
)

// jitteredTTL spreads expiry by up to a quarter of the span. A page's worth of
// covers fails at the same moment and would otherwise expire at the same moment,
// turning every retry into a synchronized burst against one site.
func jitteredTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	return ttl + time.Duration(rand.Int64N(int64(ttl/4)+1))
}

// fetchCoverURL resolves a cover for a tracker, trying its primary source first
// and then each alternate linked source. The result is cached under the primary
// key either way, so a cover found on a mirror still serves the tracker whose
// primary site is unreachable.
func (h *DashboardHandler) fetchCoverURL(parent context.Context, sourceKey, sourceURL string, sourceItemID *string, alternates []repository.TrackerSourceRef) (string, error) {
	trimmedSourceKey := strings.TrimSpace(sourceKey)
	if trimmedSourceKey == "" {
		return "", fmt.Errorf("missing source key")
	}

	cacheKey := buildCoverCacheKey(trimmedSourceKey, sourceURL, sourceItemID)
	if cachedURL, found, ok := h.getCachedCover(cacheKey); ok {
		if found {
			return cachedURL, nil
		}
		return "", fmt.Errorf("cover not found")
	}

	resolvedURL := strings.TrimSpace(sourceURL)
	if resolvedURL == "" {
		h.setCachedCover(cacheKey, "", false, jitteredTTL(lookupUnreachableTTL))
		return "", fmt.Errorf("missing source url")
	}

	tryKeys := make([]string, 0, 2)
	tryKeys = append(tryKeys, trimmedSourceKey)

	if fallbackKey := inferSourceKeyFromURL(resolvedURL); fallbackKey != "" && fallbackKey != trimmedSourceKey {
		tryKeys = append(tryKeys, fallbackKey)
	}

	// A source that was actually queried and failed may succeed shortly; one with
	// no usable connector will not, so the two are cached for different spans —
	// the same split fetchChapterURL makes.
	attempted := false

	for _, key := range tryKeys {
		coverURL, tried, err := h.resolveCoverFromConnector(parent, key, resolvedURL)
		attempted = attempted || tried
		if err != nil || coverURL == "" {
			continue
		}
		if served, ok := h.cacheCoverResult(parent, cacheKey, coverURL, trimmedSourceKey); ok {
			return served, nil
		}
	}

	// The primary source could not supply a cover. Fall back to the tracker's
	// other linked sources, which is what keeps a library readable when a site
	// goes behind a bot challenge.
	for _, alternate := range alternates {
		alternateURL := strings.TrimSpace(alternate.SourceURL)
		alternateKey := strings.TrimSpace(alternate.SourceKey)
		if alternateURL == "" || alternateKey == "" {
			continue
		}

		coverURL, tried, err := h.resolveCoverFromConnector(parent, alternateKey, alternateURL)
		attempted = attempted || tried
		if err != nil || coverURL == "" {
			continue
		}
		if served, ok := h.cacheCoverResult(parent, cacheKey, coverURL, alternateKey); ok {
			return served, nil
		}
	}

	negativeTTL := lookupUnreachableTTL
	if attempted {
		negativeTTL = lookupRetryTTL
	}
	h.setCachedCover(cacheKey, "", false, jitteredTTL(negativeTTL))
	return "", fmt.Errorf("cover not found")
}

// coverProbeClient fetches candidate cover images. It goes through the
// shared throttle like everything else; image CDNs are their own hosts, so
// the pacing does not compete with API traffic.
var coverProbeClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: connectors.ThrottleTransport(nil),
}

// guardedCoverClient is the client the running app fetches covers with. A cover
// URL is scraped from a third-party page and the image it returns is then
// republished under /covers, so an unrestricted fetch hands whoever controls a
// source a request into the Pi's own services and the rest of the LAN. Three
// checks stand between them: the transport refuses anything that is not https
// to a name resolving entirely to public addresses, the dialer refuses the
// address the connection is actually being made to, and CheckRedirect refuses a
// hop before it is issued.
var guardedCoverClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &publicHostTransport{base: connectors.ThrottleTransport(publicAddressTransport())},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after %d redirects", len(via))
		}
		return checkCoverTarget(req.Context(), req.URL)
	},
}

// coverFetchClient is what cover downloads and probes go through. A handler
// built by struct literal has no client and falls back to the unguarded one,
// which is what lets the tests serve their fixtures from 127.0.0.1.
func (h *DashboardHandler) coverFetchClient() *http.Client {
	if h.coverClient != nil {
		return h.coverClient
	}
	return coverProbeClient
}

// publicHostTransport refuses a request before the throttle claims a pacing
// slot for it. It sits outside the throttle so a refused target never delays a
// real fetch, and it runs on every request the client makes, redirects included.
type publicHostTransport struct {
	base http.RoundTripper
}

func (t *publicHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkCoverTarget(req.Context(), req.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// publicAddressTransport dials public addresses only. Unlike the name-based
// check this one sees the address the connection is actually being made to, so
// a name that answers with a public address and then a loopback one is refused
// on the answer that would have been used.
func publicAddressTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("cover fetch: unusable dial address %q", address)
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("cover fetch: unusable dial address %q", address)
			}
			if !isPublicAddr(addr) {
				return fmt.Errorf("cover fetch: refusing to connect to %s", addr)
			}
			return nil
		},
	}).DialContext
	return transport
}

// checkCoverTarget rejects a cover URL that is not https or that names a host
// with any non-public answer. All answers must be public: a name that resolves
// to both is a rebind attempt, not a CDN.
func checkCoverTarget(ctx context.Context, target *url.URL) error {
	if target == nil || !strings.EqualFold(target.Scheme, "https") {
		return fmt.Errorf("cover fetch: refusing non-https url")
	}

	host := target.Hostname()
	if host == "" {
		return fmt.Errorf("cover fetch: url without a host")
	}

	if literal, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(literal) {
			return fmt.Errorf("cover fetch: refusing address %s", literal)
		}
		return nil
	}

	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("cover fetch: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("cover fetch: %s resolves to nothing", host)
	}
	for _, addr := range addrs {
		if !isPublicAddr(addr) {
			return fmt.Errorf("cover fetch: %s resolves to %s", host, addr)
		}
	}
	return nil
}

// isPublicAddr reports whether an address is one the public internet routes to.
// Everything it rejects — loopback, RFC1918 and unique-local, link-local,
// multicast, unspecified — is this machine or this network.
func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	switch {
	case addr.IsLoopback(),
		addr.IsPrivate(),
		addr.IsUnspecified(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast():
		return false
	}
	return true
}

// coverLocalURLPrefix is where the router serves the files under coverDir.
const coverLocalURLPrefix = "/covers/"

// coverLocalTTL is the nominal expiry stamped on entries whose image lives on
// disk. They are exempt from every expiry check — the file is the cache and
// lives as long as the tracker — so the value only keeps the column non-null.
const coverLocalTTL = 10 * 365 * 24 * time.Hour

// coverDownloadLimit caps how much of a claimed cover is read. Real covers
// run tens to a few hundred KB; anything past the cap is not a cover.
const coverDownloadLimit = 8 << 20

// cacheCoverResult validates a resolved cover URL and caches it, returning
// the URL the card should serve. With a local store configured the image is
// downloaded once and served from this host from then on — the download
// doubles as the "does this URL actually load" probe. Without one (tests
// inject a checker; the store can fail at startup) it degrades to the old
// behavior: probe one byte and hotlink the source CDN for the TTL.
func (h *DashboardHandler) cacheCoverResult(parent context.Context, cacheKey, coverURL, sourceKey string) (string, bool) {
	if h.coverURLChecker != nil {
		if !h.coverURLChecker(parent, coverURL) {
			return "", false
		}
	} else if h.coverDir != "" {
		localPath, ok := h.storeCoverLocally(parent, coverURL)
		if !ok {
			return "", false
		}
		h.setCachedCoverLocal(cacheKey, coverURL, localPath, sourceKey)
		return coverLocalURLPrefix + localPath, true
	} else if !h.probeCoverURL(parent, coverURL) {
		return "", false
	}

	h.setCachedCoverFromSource(cacheKey, coverURL, sourceKey, true, 12*time.Hour)
	return coverURL, true
}

// storeCoverLocally downloads a cover image into coverDir and returns the
// file name it was stored under. The name is the hash of the remote URL, so
// the same art shared by several trackers is stored once, and the /covers
// route can serve it as immutable — a source that changes its art publishes
// a new URL, which becomes a new file.
func (h *DashboardHandler) storeCoverLocally(parent context.Context, coverURL string) (string, bool) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")

	res, err := h.coverFetchClient().Do(req)
	if err != nil {
		slog.Debug("cover download failed", "url", coverURL, "error", err)
		return "", false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, coverDownloadLimit+1))
	if err != nil || len(body) == 0 || len(body) > coverDownloadLimit {
		return "", false
	}

	ext := coverFileExt(res.Header.Get("Content-Type"), body)
	if ext == "" {
		// Not recognizably an image — an HTML challenge page, a CDN error
		// body. Caching it would serve garbage forever.
		return "", false
	}

	name := fmt.Sprintf("%x%s", sha1.Sum([]byte(coverURL)), ext)
	target := filepath.Join(h.coverDir, name)
	if _, statErr := os.Stat(target); statErr == nil {
		return name, true
	}

	// Write-then-rename so a concurrent fetch of the same URL never serves a
	// half-written file. A rename that loses the race to an identical file is
	// a success.
	temp, err := os.CreateTemp(h.coverDir, name+".tmp*")
	if err != nil {
		return "", false
	}
	tempName := temp.Name()
	_, writeErr := temp.Write(body)
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tempName)
		return "", false
	}
	if err := os.Rename(tempName, target); err != nil {
		_ = os.Remove(tempName)
		if _, statErr := os.Stat(target); statErr != nil {
			return "", false
		}
	}
	return name, true
}

// coverFileExt maps a downloaded cover to the extension the static route
// serves it under, trusting the declared content type first and the bytes
// second. An empty result means "not an image we recognize".
func coverFileExt(contentType string, body []byte) string {
	declared := strings.ToLower(strings.TrimSpace(contentType))
	if semicolon := strings.IndexByte(declared, ';'); semicolon >= 0 {
		declared = strings.TrimSpace(declared[:semicolon])
	}
	byType := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"image/avif": ".avif",
	}
	if ext, ok := byType[declared]; ok {
		return ext
	}
	if ext, ok := byType[strings.ToLower(http.DetectContentType(body))]; ok {
		return ext
	}
	return ""
}

// probeCoverURL asks the image host for the first byte of the file. The
// timeout is deliberately short next to the resolve timeout: the failure mode
// this guards against is a dead CDN that hangs the connection (not a slow
// answer), and every broken cover on a page pays it serially per host.
func (h *DashboardHandler) probeCoverURL(parent context.Context, coverURL string) bool {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	req.Header.Set("Range", "bytes=0-0")

	res, err := h.coverFetchClient().Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))

	// Any refusal counts as broken: the dashboard hotlinks covers with the
	// reader's plain browser request, so a host that turns us down here would
	// turn the <img> tag down too.
	return res.StatusCode >= 200 && res.StatusCode < 300
}

// resolveCoverFromConnector also reports whether a connector was actually
// queried. "No connector registered for this key" and "the site refused us" are
// both failures here, but only the second is worth retrying soon.
func (h *DashboardHandler) resolveCoverFromConnector(parent context.Context, sourceKey, sourceURL string) (string, bool, error) {
	connector, ok := h.registry.Get(strings.TrimSpace(sourceKey))
	if !ok {
		return "", false, fmt.Errorf("connector not found")
	}

	// Generous because these fetches run in background goroutines and the
	// shared throttle makes a page-load's worth of them queue single-file per
	// host: the last of two dozen covers legitimately waits tens of seconds
	// for its slot, and cutting it down just caches "no cover" for ten
	// minutes.
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, sourceURL)
	if err != nil {
		return "", true, err
	}
	if result == nil {
		return "", true, fmt.Errorf("empty result")
	}

	return strings.TrimSpace(result.CoverImageURL), true, nil
}

func inferSourceKeyFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	switch {
	case strings.Contains(host, "mangadex"):
		return "mangadex"
	case strings.Contains(host, "mangafire"):
		return "mangafire"
	case strings.Contains(host, "mgeko"):
		return "mgeko"
	case strings.Contains(host, "asura"):
		return "asuracomic"
	case strings.Contains(host, "flame"):
		return "flamecomics"
	case strings.Contains(host, "webtoons"):
		return "webtoons"
	case strings.Contains(host, "freewebnovel"):
		return "freewebnovel"
	case strings.Contains(host, "mangaupdates"):
		return "mangaupdates"
	case strings.Contains(host, "comick"):
		return "comick"
	case strings.Contains(host, "mangahub"):
		return "mangahub"
	default:
		return ""
	}
}

func buildCoverCacheKey(sourceKey, sourceURL string, sourceItemID *string) string {
	itemID := ""
	if sourceItemID != nil {
		itemID = strings.TrimSpace(*sourceItemID)
	}

	base := strings.ToLower(strings.TrimSpace(sourceKey)) + "|"
	if itemID != "" {
		return base + "item:" + strings.ToLower(itemID)
	}

	trimmedURL := strings.TrimSpace(sourceURL)
	if trimmedURL != "" {
		return base + "url:" + strings.ToLower(trimmedURL)
	}

	return base + "missing"
}

func (h *DashboardHandler) getCachedCover(titleID string) (coverURL string, found bool, ok bool) {
	coverURL, _, found, ok = h.getCachedCoverWithSource(titleID)
	return coverURL, found, ok
}

// getCachedCoverWithSource also reports which source supplied the cached cover.
// An entry with a local copy never expires, but it is only as good as its file:
// one whose file vanished is dropped so the next render re-fetches the cover.
func (h *DashboardHandler) getCachedCoverWithSource(titleID string) (coverURL string, sourceKey string, found bool, ok bool) {
	h.cacheMu.RLock()
	entry, exists := h.coverCache[titleID]
	h.cacheMu.RUnlock()
	if !exists {
		return "", "", false, false
	}

	if entry.LocalPath != "" {
		if _, err := os.Stat(filepath.Join(h.coverDir, entry.LocalPath)); err == nil {
			return coverLocalURLPrefix + entry.LocalPath, entry.SourceKey, true, true
		}
		h.dropCachedCover(titleID)
		return "", "", false, false
	}

	if time.Now().UTC().After(entry.ExpiresAt) {
		h.dropCachedCover(titleID)
		return "", "", false, false
	}

	return entry.CoverURL, entry.SourceKey, entry.Found, true
}

func (h *DashboardHandler) dropCachedCover(titleID string) {
	h.cacheMu.Lock()
	delete(h.coverCache, titleID)
	h.cacheMu.Unlock()
	if h.coverStore != nil {
		if err := h.coverStore.Delete(titleID); err != nil {
			slog.Debug("cover cache delete failed", "error", err)
		}
	}
}

func (h *DashboardHandler) setCachedCover(titleID, coverURL string, found bool, ttl time.Duration) {
	h.setCachedCoverFromSource(titleID, coverURL, "", found, ttl)
}

func (h *DashboardHandler) setCachedCoverFromSource(titleID, coverURL, sourceKey string, found bool, ttl time.Duration) {
	h.putCoverCacheEntry(titleID, coverCacheEntry{
		CoverURL:  coverURL,
		Found:     found,
		SourceKey: strings.TrimSpace(sourceKey),
		ExpiresAt: time.Now().UTC().Add(ttl),
	})
}

// setCachedCoverLocal records a cover whose image now lives on disk. The
// remote URL is kept alongside for reference, but the entry serves the local
// copy and is exempt from expiry.
func (h *DashboardHandler) setCachedCoverLocal(titleID, coverURL, localPath, sourceKey string) {
	h.putCoverCacheEntry(titleID, coverCacheEntry{
		CoverURL:  coverURL,
		Found:     true,
		SourceKey: strings.TrimSpace(sourceKey),
		ExpiresAt: time.Now().UTC().Add(coverLocalTTL),
		LocalPath: localPath,
	})
}

func (h *DashboardHandler) putCoverCacheEntry(titleID string, entry coverCacheEntry) {
	h.cacheMu.Lock()
	h.coverCache[titleID] = entry
	h.cacheMu.Unlock()

	// Write-through so the entry survives restarts; best-effort because a
	// failed persist only costs a re-resolve after the next restart.
	if h.coverStore != nil {
		if err := h.coverStore.Upsert(repository.CoverCacheRow{
			CacheKey:  titleID,
			CoverURL:  entry.CoverURL,
			SourceKey: entry.SourceKey,
			Found:     entry.Found,
			ExpiresAt: entry.ExpiresAt,
			LocalPath: entry.LocalPath,
		}); err != nil {
			slog.Debug("cover cache persist failed", "error", err)
		}
	}
}

// invalidateLinkLookups drops the chapter-URL cache and every negative cover
// entry. Called whenever a tracker's linked sources or reading pin change: a
// "no link found" computed before a source was attached would otherwise
// outlive the attachment by its whole TTL, which reads as the new link not
// working. Found covers stay — a new link cannot make a good cover worse.
func (h *DashboardHandler) invalidateLinkLookups() {
	h.chapterURLCacheMu.Lock()
	h.chapterURLCache = make(map[string]chapterURLCacheEntry)
	h.chapterURLCacheMu.Unlock()

	h.cacheMu.Lock()
	for key, entry := range h.coverCache {
		if !entry.Found {
			delete(h.coverCache, key)
		}
	}
	h.cacheMu.Unlock()

	if h.coverStore != nil {
		if err := h.coverStore.DeleteNegatives(); err != nil {
			slog.Debug("cover cache negative sweep failed", "error", err)
		}
	}
}

// assetURL stamps a static asset's URL with its last-modified time. The static
// handler sends no Cache-Control and no ETag, only Last-Modified, which leaves
// browsers free to guess a freshness lifetime from the file's age — so a script
// that had sat unchanged for months could keep being served from cache for days
// after a deploy, running old code against a new server. A URL that changes when
// the file changes is what makes a deploy actually take effect.
func assetURL(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")

	assetVersionMu.Lock()
	defer assetVersionMu.Unlock()

	if version, cached := assetVersions[name]; cached {
		return "/assets/" + name + version
	}

	version := ""
	if info, err := os.Stat(filepath.Join("web", "assets", filepath.FromSlash(name))); err == nil {
		version = "?v=" + strconv.FormatInt(info.ModTime().Unix(), 10)
	}
	assetVersions[name] = version
	return "/assets/" + name + version
}

var (
	assetVersionMu sync.Mutex
	assetVersions  = map[string]string{}
)

func parseDashboardTemplates() (*template.Template, error) {
	return template.New("").Funcs(template.FuncMap{
		"chapterInputValue": chapterInputValue,
		"textInputValue":    textInputValue,
		"timeInputValue":    timeInputValue,
		"hasTagID":          hasTagID,
		"tagIconLabel":      tagIconLabel,
		"tagIconAssetPath":  tagIconAssetPath,
		"toJSON":            toJSON,
		"statusLabel":       statusLabel,
		"sortLabel":         sortLabel,
		"assetURL":          assetURL,
	}).ParseGlob("web/templates/*.html")
}

// renderBuffers keeps the page-sized buffers below off the allocator. The Pi
// renders the same handful of templates over and over, so the buffers are worth
// reusing; an unusually large one is dropped instead of being held forever by
// the pool.
var renderBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

const renderBufferKeepLimit = 1 << 20

// render executes into a buffer and copies to the response only once the whole
// page is built. Writing straight into the response body could not be taken
// back: a template that failed halfway left a truncated page already sent with
// status 200, and the error the handler returned changed nothing the reader saw.
func (h *DashboardHandler) render(c *fiber.Ctx, templateName string, data any) error {
	if h.templates == nil {
		slog.Error("dashboard templates unavailable", "template", templateName, "error", h.templateErr)
		return c.Status(fiber.StatusInternalServerError).SendString("Template load error")
	}

	buffer := renderBuffers.Get().(*bytes.Buffer)
	buffer.Reset()
	defer func() {
		if buffer.Cap() <= renderBufferKeepLimit {
			renderBuffers.Put(buffer)
		}
	}()

	if err := h.templates.ExecuteTemplate(buffer, templateName, data); err != nil {
		slog.Error("template render failed", "template", templateName, "error", err)
		return c.Status(fiber.StatusInternalServerError).SendString("Template render error")
	}

	c.Type("html", "utf-8")
	// Write appends a copy; Send would hand fasthttp the pooled buffer itself,
	// which is reused as soon as this returns.
	_, err := c.Write(buffer.Bytes())
	return err
}

func statusLabel(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "all":
		return "All statuses"
	case "on_hold":
		return "On hold"
	case "plan_to_read":
		return "Plan to read"
	default:
		return humanizeValueLabel(value)
	}
}

func sortLabel(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "last_read_at":
		return "Recently read"
	case "title":
		return "Title (A–Z)"
	case "created_at":
		return "Date added"
	case "last_checked_at":
		return "Last checked"
	case "rating":
		return "Rating"
	case "latest_known_chapter":
		return "Latest chapter"
	default:
		return humanizeValueLabel(value)
	}
}

func humanizeValueLabel(value string) string {
	normalized := strings.TrimSpace(strings.ToLower(value))
	if normalized == "" {
		return "—"
	}
	parts := strings.Fields(valueLabelReplacer.Replace(normalized))
	for index, part := range parts {
		if len(part) == 0 {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func toJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func hasTagID(tags []models.CustomTag, id int64) bool {
	for _, tag := range tags {
		if tag.ID == id {
			return true
		}
	}
	return false
}

func availableTagIconKeys(profileTags []models.CustomTag) []string {
	used := make(map[string]bool, len(profileTags))
	for _, tag := range profileTags {
		if tag.IconKey == nil {
			continue
		}
		iconKey := strings.TrimSpace(*tag.IconKey)
		if iconKey != "" {
			used[iconKey] = true
		}
	}

	available := make([]string, 0, len(tagIconKeysOrdered))
	for _, iconKey := range tagIconKeysOrdered {
		if used[iconKey] {
			continue
		}
		available = append(available, iconKey)
	}

	return available
}

func tagIconLabel(iconKey string) string {
	switch strings.TrimSpace(iconKey) {
	case "icon_1":
		return "Star"
	case "icon_2":
		return "Heart"
	case "icon_3":
		return "Flames"
	default:
		return "Icon"
	}
}

// tagIconAssetPath is the templates' name for the one key-to-path map, which
// lives with the model because the tag chips read the path off the tag itself.
func tagIconAssetPath(iconKey string) string {
	return models.TagIconAssetPath(iconKey)
}
