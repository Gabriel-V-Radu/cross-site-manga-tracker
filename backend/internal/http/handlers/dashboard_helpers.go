package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// sourceHomeURLForKey is the site root a source's shortcut opens. The address
// comes from the connector that reads the site, so a site that moves domain
// takes the dashboard with it instead of leaving a shortcut pointing at the
// address it was renamed away from. A key no connector claims has no home to
// offer: the caller then falls back to the source row's own base URL, and drops
// the shortcut when there is none — the behavior an unlisted key always had.
func (h *DashboardHandler) sourceHomeURLForKey(sourceKey string) string {
	info, ok := h.siteInfoForKey(sourceKey)
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.HomeURL())
}

// siteInfoForKey resolves a source key to the metadata its connector publishes.
// A handler built without a registry — the card-building tests construct one by
// struct literal — resolves nothing rather than panicking.
func (h *DashboardHandler) siteInfoForKey(sourceKey string) (connectors.SiteInfo, bool) {
	if h.registry == nil {
		return nil, false
	}
	connector, ok := h.registry.Get(strings.TrimSpace(sourceKey))
	if !ok {
		return nil, false
	}
	info, ok := connector.(connectors.SiteInfo)
	return info, ok
}

// connectorForURL resolves the connector that claims a URL's host, through the
// hosts the connectors themselves publish. Nil-registry safe for the same
// reason as siteInfoForKey.
func (h *DashboardHandler) connectorForURL(rawURL string) (connectors.Connector, bool) {
	if h.registry == nil {
		return nil, false
	}
	return h.registry.GetByURL(rawURL)
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
		return countedAgo(int(delta/time.Hour), "hour")
	}
	if delta < 30*24*time.Hour {
		return countedAgo(int(delta/(24*time.Hour)), "day")
	}
	if delta < 365*24*time.Hour {
		return countedAgo(int(delta/(30*24*time.Hour)), "month")
	}
	return countedAgo(int(delta/(365*24*time.Hour)), "year")
}

// countedAgo spells a whole-unit age. The card shows a release date on every
// row, so the one-unit case is common enough that "1 hours ago" was on screen
// most of the time.
func countedAgo(count int, unit string) string {
	if count == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", count, unit)
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

// sourceKeyForURL names the site a URL belongs to. The registry matches the
// hosts each connector claims, historical domains included, where this was a
// second host table that matched by substring — and so also claimed any host
// that merely contained "asura" or "flame" for the real site's connector.
func (h *DashboardHandler) sourceKeyForURL(rawURL string) string {
	connector, ok := h.connectorForURL(rawURL)
	if !ok {
		return ""
	}
	return connector.Key()
}

// invalidateLinkLookups drops the chapter-URL cache and every negative cover
// entry. Called whenever a tracker's linked sources or reading pin change: a
// "no link found" computed before a source was attached would otherwise
// outlive the attachment by its whole TTL, which reads as the new link not
// working. Found covers stay — a new link cannot make a good cover worse.
func (h *DashboardHandler) invalidateLinkLookups() {
	h.chapterLinks.Invalidate()
	h.covers.InvalidateNegatives()
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
