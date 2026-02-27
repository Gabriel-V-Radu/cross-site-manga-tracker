package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
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
		return "https://asuracomic.net"
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

func (h *DashboardHandler) fetchCoverURL(parent context.Context, sourceKey, sourceURL string, sourceItemID *string) (string, error) {
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
		h.setCachedCover(cacheKey, "", false, 2*time.Minute)
		return "", fmt.Errorf("missing source url")
	}

	tryKeys := make([]string, 0, 2)
	tryKeys = append(tryKeys, trimmedSourceKey)

	if fallbackKey := inferSourceKeyFromURL(resolvedURL); fallbackKey != "" && fallbackKey != trimmedSourceKey {
		tryKeys = append(tryKeys, fallbackKey)
	}

	for _, key := range tryKeys {
		coverURL, err := h.resolveCoverFromConnector(parent, key, resolvedURL)
		if err != nil {
			continue
		}
		if coverURL == "" {
			continue
		}

		h.setCachedCover(cacheKey, coverURL, true, 12*time.Hour)
		return coverURL, nil
	}

	missTTL := 2 * time.Minute
	if strings.EqualFold(trimmedSourceKey, "mangafire") {
		missTTL = 25 * time.Second
	}
	h.setCachedCover(cacheKey, "", false, missTTL)
	return "", fmt.Errorf("cover not found")
}

func (h *DashboardHandler) resolveCoverFromConnector(parent context.Context, sourceKey, sourceURL string) (string, error) {
	connector, ok := h.registry.Get(strings.TrimSpace(sourceKey))
	if !ok {
		return "", fmt.Errorf("connector not found")
	}

	resolveTimeout := 8 * time.Second
	if strings.EqualFold(strings.TrimSpace(sourceKey), "mangafire") {
		resolveTimeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, resolveTimeout)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, sourceURL)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("empty result")
	}

	return strings.TrimSpace(result.CoverImageURL), nil
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
	h.cacheMu.RLock()
	entry, exists := h.coverCache[titleID]
	h.cacheMu.RUnlock()
	if !exists {
		return "", false, false
	}

	if time.Now().UTC().After(entry.ExpiresAt) {
		h.cacheMu.Lock()
		delete(h.coverCache, titleID)
		h.cacheMu.Unlock()
		return "", false, false
	}

	return entry.CoverURL, entry.Found, true
}

func (h *DashboardHandler) setCachedCover(titleID, coverURL string, found bool, ttl time.Duration) {
	h.cacheMu.Lock()
	h.coverCache[titleID] = coverCacheEntry{
		CoverURL:  coverURL,
		Found:     found,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	h.cacheMu.Unlock()
}

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
