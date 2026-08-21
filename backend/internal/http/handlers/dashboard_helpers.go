package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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
			IconPath: tag.IconPath,
		})
	}
	return items
}

func toTrackerTagIcons(tags []models.CustomTag) []trackerTagIconView {
	icons := make([]trackerTagIconView, 0, len(tags))
	for _, tag := range tags {
		if tag.IconPath == nil {
			continue
		}
		icons = append(icons, trackerTagIconView{TagName: tag.Name, IconPath: *tag.IconPath})
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
	case "mangabuddy":
		return "https://mangabuddy1.co.uk"
	case "mgeko":
		return "https://www.mgeko.cc"
	case "webtoons":
		return "https://www.webtoons.com"
	case "freewebnovel":
		return "https://freewebnovel.com"
	case "weebcentral":
		return "https://weebcentral.com"
	case "mangaupdates":
		return "https://www.mangaupdates.com"
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
		if tag.IconPath != nil {
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
		if err != nil {
			continue
		}
		if coverURL == "" {
			continue
		}

		h.setCachedCoverFromSource(cacheKey, coverURL, trimmedSourceKey, true, 12*time.Hour)
		return coverURL, nil
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

		h.setCachedCoverFromSource(cacheKey, coverURL, alternateKey, true, 12*time.Hour)
		return coverURL, nil
	}

	negativeTTL := lookupUnreachableTTL
	if attempted {
		negativeTTL = lookupRetryTTL
	}
	h.setCachedCover(cacheKey, "", false, jitteredTTL(negativeTTL))
	return "", fmt.Errorf("cover not found")
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
	case strings.Contains(host, "mangabuddy"):
		return "mangabuddy"
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
	case strings.Contains(host, "weebcentral"):
		return "weebcentral"
	case strings.Contains(host, "mangaupdates"):
		return "mangaupdates"
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
func (h *DashboardHandler) getCachedCoverWithSource(titleID string) (coverURL string, sourceKey string, found bool, ok bool) {
	h.cacheMu.RLock()
	entry, exists := h.coverCache[titleID]
	h.cacheMu.RUnlock()
	if !exists {
		return "", "", false, false
	}

	if time.Now().UTC().After(entry.ExpiresAt) {
		h.cacheMu.Lock()
		delete(h.coverCache, titleID)
		h.cacheMu.Unlock()
		return "", "", false, false
	}

	return entry.CoverURL, entry.SourceKey, entry.Found, true
}

func (h *DashboardHandler) setCachedCover(titleID, coverURL string, found bool, ttl time.Duration) {
	h.setCachedCoverFromSource(titleID, coverURL, "", found, ttl)
}

func (h *DashboardHandler) setCachedCoverFromSource(titleID, coverURL, sourceKey string, found bool, ttl time.Duration) {
	h.cacheMu.Lock()
	h.coverCache[titleID] = coverCacheEntry{
		CoverURL:  coverURL,
		Found:     found,
		SourceKey: strings.TrimSpace(sourceKey),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	h.cacheMu.Unlock()
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

func (h *DashboardHandler) render(c *fiber.Ctx, templateName string, data any) error {
	h.templateOnce.Do(func() {
		h.templates, h.templateErr = template.New("").Funcs(template.FuncMap{
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
	})

	if h.templateErr != nil || h.templates == nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Template load error")
	}
	c.Type("html", "utf-8")
	return h.templates.ExecuteTemplate(c.Response().BodyWriter(), templateName, data)
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

func tagIconAssetPath(iconKey string) string {
	switch strings.TrimSpace(iconKey) {
	case "icon_1":
		return "/assets/tag-icons/icon-star-gold.svg"
	case "icon_2":
		return "/assets/tag-icons/icon-red-heart.svg"
	case "icon_3":
		return "/assets/tag-icons/icon-flames.svg"
	default:
		return ""
	}
}
