package handlers

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gofiber/fiber/v2"
)

const (
	sourceLogoUploadDir      = "data/uploads/site-logos"
	sourceLogoPublicPrefix   = "/uploads/site-logos/"
	maxSourceLogoUploadBytes = 2 << 20 // 2MB
	maxSourceLogoUploadLabel = "2MB"
)

func (h *DashboardHandler) Page(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	linkedSites, err := h.listLinkedSourcesForProfile(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sites")
	}
	selectedLinkedSiteIDs := sourceIDFilterMap(parseSourceIDsFromQuery(c))

	c.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Set("Pragma", "no-cache")
	c.Set("Expires", "0")
	data := dashboardPageData{
		Statuses:              []string{"all", "reading", "completed", "on_hold", "dropped", "plan_to_read"},
		Sorts:                 []string{"latest_known_chapter", "last_read_at", "rating"},
		Profiles:              profiles,
		ActiveProfile:         *activeProfile,
		RenameValue:           activeProfile.Name,
		ProfileTags:           profileTags,
		LinkedSites:           linkedSites,
		SelectedLinkedSiteIDs: selectedLinkedSiteIDs,
	}
	return h.render(c, "dashboard_page.html", data)
}

func (h *DashboardHandler) RenameProfileFromForm(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	name := strings.TrimSpace(c.FormValue("profile_name"))
	if name == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Profile name is required")
	}
	if len(name) > 40 {
		return c.Status(fiber.StatusBadRequest).SendString("Profile name must be 40 characters or less")
	}

	if _, err := h.profileRepo.Rename(activeProfile.ID, name); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to rename profile")
	}

	return c.Redirect("/dashboard?profile="+url.QueryEscape(activeProfile.Key), fiber.StatusSeeOther)
}

func (h *DashboardHandler) ProfileMenuModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	return h.renderProfileMenu(c, activeProfile, "", "")
}

func (h *DashboardHandler) ProfileFilterTagsPartial(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	return h.render(c, "profile_filter_tags_partial.html", profileFilterTagsData{ProfileTags: profileTags})
}

func (h *DashboardHandler) ProfileFilterLinkedSitesPartial(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	linkedSites, err := h.listLinkedSourcesForProfile(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sites")
	}

	return h.render(c, "profile_filter_linked_sites_partial.html", profileFilterLinkedSitesData{
		LinkedSites:       linkedSites,
		SelectedSourceIDs: sourceIDFilterMap(parseSourceIDsFromQuery(c)),
	})
}

func (h *DashboardHandler) SwitchProfileFromMenu(c *fiber.Ctx) error {
	profileKey := strings.TrimSpace(string(c.Request().PostArgs().Peek("profile")))
	if profileKey == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Profile is required")
	}

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	for _, profile := range profiles {
		if profile.Key == profileKey {
			return c.Redirect("/dashboard?profile="+url.QueryEscape(profileKey), fiber.StatusSeeOther)
		}
	}

	return c.Status(fiber.StatusBadRequest).SendString("Selected profile does not exist")
}

func (h *DashboardHandler) CreateTagFromMenu(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	tagName := strings.TrimSpace(c.FormValue("tag_name"))
	if tagName == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Tag name is required")
	}
	if len(tagName) > 40 {
		return c.Status(fiber.StatusBadRequest).SendString("Tag name must be 40 characters or less")
	}

	var iconKey *string
	if rawIcon := strings.TrimSpace(c.FormValue("icon_key")); rawIcon != "" {
		if !allowedTagIconKeys[rawIcon] {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid icon")
		}
		iconKey = &rawIcon
	}

	if _, err := h.trackerRepo.CreateProfileTag(activeProfile.ID, tagName, iconKey); err != nil {
		lowerErr := strings.ToLower(err.Error())
		if strings.Contains(lowerErr, "unique") {
			if iconKey != nil {
				return c.Status(fiber.StatusBadRequest).SendString("That icon is already used by another tag")
			}
			return c.Status(fiber.StatusBadRequest).SendString("A tag with that name already exists")
		}
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save tag")
	}

	return h.renderProfileMenu(c, activeProfile, "Tag saved", `{"trackersChanged":true,"profileTagsChanged":true}`)
}

func (h *DashboardHandler) RenameTagFromMenu(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	tagID, err := strconv.ParseInt(strings.TrimSpace(c.FormValue("tag_id")), 10, 64)
	if err != nil || tagID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tag")
	}

	tagName := strings.TrimSpace(c.FormValue("tag_name"))
	if tagName == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Tag name is required")
	}
	if len(tagName) > 40 {
		return c.Status(fiber.StatusBadRequest).SendString("Tag name must be 40 characters or less")
	}

	renamed, err := h.trackerRepo.RenameProfileTag(activeProfile.ID, tagID, tagName)
	if err != nil {
		lowerErr := strings.ToLower(err.Error())
		if strings.Contains(lowerErr, "unique") {
			return c.Status(fiber.StatusBadRequest).SendString("A tag with that name already exists")
		}
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to rename tag")
	}
	if !renamed {
		return c.Status(fiber.StatusBadRequest).SendString("Tag not found")
	}

	return h.renderProfileMenu(c, activeProfile, "Tag renamed", `{"trackersChanged":true,"profileTagsChanged":true}`)
}

func (h *DashboardHandler) DeleteTagFromMenu(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	tagID, err := strconv.ParseInt(strings.TrimSpace(c.FormValue("tag_id")), 10, 64)
	if err != nil || tagID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid tag")
	}

	deleted, err := h.trackerRepo.DeleteProfileTag(activeProfile.ID, tagID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to delete tag")
	}
	if !deleted {
		return c.Status(fiber.StatusBadRequest).SendString("Tag not found")
	}

	return h.renderProfileMenu(c, activeProfile, "Tag deleted", `{"trackersChanged":true,"profileTagsChanged":true}`)
}

func (h *DashboardHandler) SaveSourceLogosFromMenu(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid profile")
	}

	linkedSites, err := h.listLinkedSourcesForProfile(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sites")
	}
	if len(linkedSites) == 0 {
		return h.renderProfileMenu(c, activeProfile, "No sites available to configure", "")
	}

	existingLogosBySourceID, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked site logos")
	}

	logoBySourceID, err := readSourceLogoUpdates(c, activeProfile.ID, linkedSites, existingLogosBySourceID)
	if err != nil {
		return h.renderProfileMenu(c, activeProfile, err.Error(), "")
	}

	if err := h.sourceRepo.UpsertProfileSourceLogoURLs(activeProfile.ID, logoBySourceID); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save linked site logos")
	}

	return h.renderProfileMenu(c, activeProfile, "Linked site logos saved", `{"trackersChanged":true}`)
}

func (h *DashboardHandler) listLinkedSourcesForProfile(_ int64) ([]models.Source, error) {
	enabledSources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return nil, fmt.Errorf("list enabled sources: %w", err)
	}
	return enabledSources, nil
}

func (h *DashboardHandler) renderProfileMenu(c *fiber.Ctx, activeProfile *models.Profile, message string, hxTrigger string) error {
	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	linkedSites, err := h.listLinkedSourcesForProfile(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked sites")
	}

	sourceLogoURLs, err := h.sourceRepo.ListProfileSourceLogoURLs(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load linked site logos")
	}

	if strings.TrimSpace(hxTrigger) != "" {
		c.Set("HX-Trigger", hxTrigger)
	}

	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		LinkedSites:       linkedSites,
		SourceLogoURLs:    sourceLogoURLs,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
		Message:           message,
	})
}

func readSourceLogoUpdates(c *fiber.Ctx, profileID int64, linkedSites []models.Source, existingLogosBySourceID map[int64]string) (map[int64]string, error) {
	logoBySourceID := make(map[int64]string, len(linkedSites))
	for _, linkedSite := range linkedSites {
		fileField := fmt.Sprintf("source_logo_file_%d", linkedSite.ID)
		clearField := fmt.Sprintf("source_logo_clear_%d", linkedSite.ID)

		removeRequested := strings.TrimSpace(c.FormValue(clearField)) == "1"
		fileHeader, _ := c.FormFile(fileField)
		uploadRequested := fileHeader != nil && strings.TrimSpace(fileHeader.Filename) != ""

		if removeRequested && uploadRequested {
			return nil, fmt.Errorf("%s: choose either upload or remove", linkedSite.Name)
		}

		existingLogoPath := strings.TrimSpace(existingLogosBySourceID[linkedSite.ID])
		if removeRequested {
			if existingLogoPath != "" {
				_ = removeStoredSourceLogoFile(existingLogoPath)
			}
			logoBySourceID[linkedSite.ID] = ""
			continue
		}

		if !uploadRequested {
			continue
		}

		logoPath, err := saveUploadedSourceLogo(profileID, linkedSite.ID, fileHeader)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", linkedSite.Name, err)
		}

		if existingLogoPath != "" && existingLogoPath != logoPath {
			_ = removeStoredSourceLogoFile(existingLogoPath)
		}
		logoBySourceID[linkedSite.ID] = logoPath
	}

	return logoBySourceID, nil
}

func saveUploadedSourceLogo(profileID, sourceID int64, fileHeader *multipart.FileHeader) (string, error) {
	ext, data, err := readValidatedSourceLogo(fileHeader)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(sourceLogoUploadDir, 0o755); err != nil {
		return "", fmt.Errorf("prepare upload directory: %w", err)
	}

	fileName := fmt.Sprintf(
		"profile-%d-source-%d-%d%s",
		profileID,
		sourceID,
		time.Now().UTC().UnixNano(),
		ext,
	)
	diskPath := filepath.Join(sourceLogoUploadDir, fileName)

	if err := os.WriteFile(diskPath, data, 0o644); err != nil {
		return "", fmt.Errorf("save logo file: %w", err)
	}

	return sourceLogoPublicPrefix + fileName, nil
}

func readValidatedSourceLogo(fileHeader *multipart.FileHeader) (string, []byte, error) {
	if fileHeader == nil {
		return "", nil, fmt.Errorf("missing upload")
	}
	if fileHeader.Size > maxSourceLogoUploadBytes {
		return "", nil, fmt.Errorf("file too large (max %s)", maxSourceLogoUploadLabel)
	}

	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(fileHeader.Filename)))
	switch ext {
	case ".png", ".svg", ".jpg", ".jpeg", ".webp":
	default:
		return "", nil, fmt.Errorf("only .png, .svg, .jpg, .jpeg, or .webp files are allowed")
	}

	file, err := fileHeader.Open()
	if err != nil {
		return "", nil, fmt.Errorf("read upload: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxSourceLogoUploadBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > maxSourceLogoUploadBytes {
		return "", nil, fmt.Errorf("file too large (max %s)", maxSourceLogoUploadLabel)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("empty upload")
	}

	sniffLen := min(512, len(data))
	contentType := http.DetectContentType(data[:sniffLen])

	if ext == ".png" {
		if contentType != "image/png" && !hasPNGSignature(data) {
			return "", nil, fmt.Errorf("invalid PNG file")
		}
		return ext, data, nil
	}

	if ext == ".jpg" || ext == ".jpeg" {
		if contentType != "image/jpeg" && !hasJPEGSignature(data) {
			return "", nil, fmt.Errorf("invalid JPEG file")
		}
		return ext, data, nil
	}

	if ext == ".webp" {
		if contentType != "image/webp" && !hasWEBPSignature(data) {
			return "", nil, fmt.Errorf("invalid WEBP file")
		}
		return ext, data, nil
	}

	lower := strings.ToLower(string(data))
	if !strings.Contains(lower, "<svg") {
		return "", nil, fmt.Errorf("invalid SVG file")
	}
	if strings.Contains(lower, "<script") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "onload=") || strings.Contains(lower, "onerror=") {
		return "", nil, fmt.Errorf("unsafe SVG content")
	}

	return ext, data, nil
}

func hasPNGSignature(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	signature := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for i := range signature {
		if data[i] != signature[i] {
			return false
		}
	}
	return true
}

func hasJPEGSignature(data []byte) bool {
	if len(data) < 3 {
		return false
	}
	return data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
}

func hasWEBPSignature(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	return string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

func removeStoredSourceLogoFile(publicPath string) error {
	diskPath, ok := sourceLogoPublicPathToDiskPath(publicPath)
	if !ok {
		return nil
	}
	if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func sourceLogoPublicPathToDiskPath(publicPath string) (string, bool) {
	trimmed := strings.TrimSpace(publicPath)
	if !strings.HasPrefix(trimmed, sourceLogoPublicPrefix) {
		return "", false
	}

	fileName := strings.TrimPrefix(trimmed, sourceLogoPublicPrefix)
	fileName = strings.TrimSpace(filepath.Base(fileName))
	if fileName == "" || fileName == "." {
		return "", false
	}

	return filepath.Join(sourceLogoUploadDir, fileName), true
}
