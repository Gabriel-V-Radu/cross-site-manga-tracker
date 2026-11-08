package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/metadata"
	"github.com/gofiber/fiber/v2"
)

// trackerInfoView is what the info panel shows for one tracker: its series
// metadata consolidated across the linked sites, and a link to each site.
type trackerInfoView struct {
	ID            int64
	Title         string
	AltTitles     []string
	MoreAltTitles int
	HasMetadata   bool

	Type          string
	TypeLabel     string
	Status        string
	StatusLabel   string
	Year          int
	Demographic   string
	ContentRating string

	Genres  []string
	Tags    []string
	Authors []string
	Artists []string

	DescriptionParagraphs []string
	DescriptionSource     string

	Ratings []trackerInfoRatingView
	Sites   []trackerInfoSiteView

	Sources    string
	UpdatedAgo string
}

type trackerInfoRatingView struct {
	Value  string
	Source string
	Votes  string
}

type trackerInfoSiteView struct {
	Name    string
	URL     string
	LogoURL string
}

// maxInfoAltTitles is how many alternate titles the panel lists before
// folding the rest into a count: a long-running series can carry dozens of
// translations of its name.
const maxInfoAltTitles = 4

// TrackerInfo renders the info panel for one tracker (the card cover's hover
// and click). It reads only what is stored — the consolidated metadata and
// the tracker's links — so opening it never reaches a site.
func (h *DashboardHandler) TrackerInfo(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.failProfile(c, err)
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "Invalid tracker id", err)
	}

	tracker, err := h.trackerRepo.GetByID(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load tracker", err)
	}
	if tracker == nil {
		return h.fail(c, fiber.StatusNotFound, "Tracker not found", nil)
	}

	view := trackerInfoView{ID: tracker.ID, Title: tracker.Title}
	for _, alt := range tracker.RelatedTitles {
		if strings.EqualFold(strings.TrimSpace(alt), strings.TrimSpace(tracker.Title)) {
			continue
		}
		if len(view.AltTitles) < maxInfoAltTitles {
			view.AltTitles = append(view.AltTitles, alt)
		} else {
			view.MoreAltTitles++
		}
	}

	row, err := h.metadataRepo.GetTrackerMetadata(c.Context(), activeProfile.ID, id)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load series info", err)
	}
	if row != nil {
		var consolidated metadata.Consolidated
		if json.Unmarshal([]byte(row.Payload), &consolidated) == nil && !consolidated.Empty() {
			fillTrackerInfo(&view, consolidated)
			view.UpdatedAgo = relativeTime(row.UpdatedAt)
		}
	}

	sites, err := h.trackerInfoSites(c.Context(), activeProfile.ID, id, tracker.SourceID, tracker.SourceURL)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load linked sources", err)
	}
	view.Sites = sites

	// The panel is the same until the next poll or edit; a minute of private
	// caching lets hovering back and forth over a card skip the round trip.
	c.Set("Cache-Control", "private, max-age=60")
	return h.render(c, "tracker_info_popover.html", view)
}

func fillTrackerInfo(view *trackerInfoView, consolidated metadata.Consolidated) {
	view.HasMetadata = true
	view.Type = consolidated.Type
	view.TypeLabel = metadata.TypeLabel(consolidated.Type)
	view.Status = consolidated.Status
	view.StatusLabel = metadata.StatusLabel(consolidated.Status)
	view.Year = consolidated.Year
	view.Demographic = consolidated.Demographic
	if consolidated.ContentRating != "" && consolidated.ContentRating != connectors.ContentRatingSafe {
		view.ContentRating = humanizeValueLabel(consolidated.ContentRating)
	}
	view.Genres = consolidated.Genres
	view.Tags = consolidated.Tags
	view.Authors = consolidated.Authors
	view.Artists = consolidated.Artists
	// Artists that are also the authors (a solo creator) are listed once.
	if sameNames(view.Authors, view.Artists) {
		view.Artists = nil
	}
	for _, paragraph := range strings.Split(consolidated.Description, "\n\n") {
		if text := strings.TrimSpace(paragraph); text != "" {
			view.DescriptionParagraphs = append(view.DescriptionParagraphs, text)
		}
	}
	view.DescriptionSource = consolidated.DescriptionSource
	for _, rating := range consolidated.Ratings {
		entry := trackerInfoRatingView{
			Value:  strconv.FormatFloat(rating.Rating, 'f', 1, 64),
			Source: rating.SourceName,
		}
		if entry.Source == "" {
			entry.Source = humanizeValueLabel(rating.SourceKey)
		}
		if rating.Votes > 0 {
			entry.Votes = compactCount(rating.Votes)
		}
		view.Ratings = append(view.Ratings, entry)
	}
	view.Sources = strings.Join(consolidated.Sources, ", ")
}

func sameNames(a, b []string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for index := range a {
		if !strings.EqualFold(a[index], b[index]) {
			return false
		}
	}
	return true
}

// compactCount prints a vote count the way it is read at a glance: 57, 3.1k,
// 39k.
func compactCount(count int) string {
	switch {
	case count < 1000:
		return strconv.Itoa(count)
	case count < 10000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(count)/1000), ".0") + "k"
	default:
		return strconv.Itoa(count/1000) + "k"
	}
}

// trackerInfoSites lists one link per site the tracker is linked to, the
// primary first, each labelled with the site's name and logo.
func (h *DashboardHandler) trackerInfoSites(ctx context.Context, profileID, trackerID, primarySourceID int64, primaryURL string) ([]trackerInfoSiteView, error) {
	linked, err := h.trackerRepo.ListTrackerSources(ctx, profileID, trackerID)
	if err != nil {
		return nil, err
	}
	sourceByID, err := h.listSourcesByID(ctx)
	if err != nil {
		return nil, err
	}
	logos, err := h.sourceRepo.ListSourceLogoURLs(ctx)
	if err != nil {
		return nil, err
	}

	type siteLink struct {
		sourceID int64
		url      string
	}
	links := []siteLink{{sourceID: primarySourceID, url: primaryURL}}
	for _, link := range linked {
		links = append(links, siteLink{sourceID: link.SourceID, url: link.SourceURL})
	}

	seen := map[int64]bool{}
	sites := make([]trackerInfoSiteView, 0, len(links))
	for _, link := range links {
		url := strings.TrimSpace(link.url)
		if link.sourceID <= 0 || url == "" || seen[link.sourceID] {
			continue
		}
		seen[link.sourceID] = true
		name := humanizeValueLabel(strconv.FormatInt(link.sourceID, 10))
		if source, ok := sourceByID[link.sourceID]; ok && strings.TrimSpace(source.Name) != "" {
			name = source.Name
		}
		sites = append(sites, trackerInfoSiteView{
			Name:    name,
			URL:     url,
			LogoURL: strings.TrimSpace(logos[link.sourceID]),
		})
	}
	return sites, nil
}

// pageFilterSelection reads the filter state from the dashboard page's own
// URL. Values that name nothing simply match nothing, and a status or sort
// outside the form's options falls back to the default, like the list does.
func pageFilterSelection(c *fiber.Ctx) pageFilters {
	selected := pageFilters{
		Query:       strings.TrimSpace(c.Query("q")),
		Status:      strings.TrimSpace(c.Query("status", "reading")),
		Sort:        strings.TrimSpace(c.Query("sort", "latest_known_chapter")),
		Page:        parsePositiveInt(c.Query("page", "1"), 1),
		Types:       queryValues(c, "types"),
		Publication: queryValues(c, "publication"),
		Genres:      queryValues(c, "genres"),
		Tags:        map[string]bool{},
	}
	for _, tag := range parseTagNamesFromQuery(c) {
		selected.Tags[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	return selected
}

// withSelectedOptions ticks the options the URL asked for and adds any it
// asked for that the library no longer carries (with no count), so the form
// shows every filter the list is actually filtered by.
func withSelectedOptions(options []filterOptionView, selected []string, label func(string) string) []filterOptionView {
	if len(selected) == 0 {
		return options
	}
	wanted := map[string]bool{}
	for _, value := range selected {
		wanted[value] = true
	}
	for index := range options {
		if wanted[options[index].Value] {
			options[index].Selected = true
			delete(wanted, options[index].Value)
		}
	}
	for _, value := range selected {
		if wanted[value] {
			name := label(value)
			if name == "" {
				name = value
			}
			options = append(options, filterOptionView{Value: value, Label: name, Selected: true})
			delete(wanted, value)
		}
	}
	return options
}

// metadataFilterOptions builds the type, publication and genre filter menus
// for a profile from what its library actually carries.
func (h *DashboardHandler) metadataFilterOptions(ctx context.Context, profileID int64) ([]filterOptionView, []filterOptionView, []filterOptionView, error) {
	facets, err := h.metadataRepo.ListMetadataFacets(ctx, profileID)
	if err != nil {
		return nil, nil, nil, err
	}

	ordered := func(order []string, counts map[string]int, label func(string) string) []filterOptionView {
		options := make([]filterOptionView, 0, len(order))
		for _, value := range order {
			if counts[value] > 0 {
				options = append(options, filterOptionView{Value: value, Label: label(value), Count: counts[value]})
			}
		}
		return options
	}
	types := ordered(metadata.SeriesTypes, facets.Types, metadata.TypeLabel)
	publication := ordered(metadata.SeriesStatuses, facets.Statuses, metadata.StatusLabel)

	genres := make([]filterOptionView, 0, len(facets.Genres))
	for genre, count := range facets.Genres {
		genres = append(genres, filterOptionView{Value: genre, Label: genre, Count: count})
	}
	sort.Slice(genres, func(i, j int) bool { return genres[i].Label < genres[j].Label })
	return types, publication, genres, nil
}
