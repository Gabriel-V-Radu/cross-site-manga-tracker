package handlers

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gofiber/fiber/v2"
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

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
	})
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

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	c.Set("HX-Trigger", `{"trackersChanged":true,"profileTagsChanged":true}`)
	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
		Message:           "Tag saved",
	})
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

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	c.Set("HX-Trigger", `{"trackersChanged":true,"profileTagsChanged":true}`)
	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
		Message:           "Tag renamed",
	})
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

	profiles, err := h.profileResolver.ListProfiles()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profiles")
	}

	profileTags, err := h.trackerRepo.ListProfileTags(activeProfile.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to load profile tags")
	}

	c.Set("HX-Trigger", `{"trackersChanged":true,"profileTagsChanged":true}`)
	return h.render(c, "profile_menu_modal.html", profileMenuData{
		Profiles:          profiles,
		ActiveProfile:     *activeProfile,
		RenameValue:       activeProfile.Name,
		ProfileTags:       profileTags,
		TagIconKeys:       tagIconKeysOrdered,
		AvailableIconKeys: availableTagIconKeys(profileTags),
		Message:           "Tag deleted",
	})
}

func (h *DashboardHandler) listLinkedSourcesForProfile(profileID int64) ([]models.Source, error) {
	linkedSourceIDs, err := h.trackerRepo.ListLinkedSourceIDs(profileID)
	if err != nil {
		return nil, fmt.Errorf("list linked source ids: %w", err)
	}
	if len(linkedSourceIDs) == 0 {
		return []models.Source{}, nil
	}

	enabledSources, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return nil, fmt.Errorf("list enabled sources: %w", err)
	}

	sourceIDSet := sourceIDFilterMap(linkedSourceIDs)
	linkedSources := make([]models.Source, 0, len(enabledSources))
	for _, source := range enabledSources {
		if !sourceIDSet[source.ID] {
			continue
		}
		linkedSources = append(linkedSources, source)
	}

	return linkedSources, nil
}
