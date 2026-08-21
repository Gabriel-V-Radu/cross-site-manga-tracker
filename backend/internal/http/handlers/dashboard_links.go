package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

// The link review section. A scan walks the profile's trackers that lack a
// link to a chosen source, searches that source for each title and stores
// scored candidates; this page is where those candidates get accepted or
// rejected, where trackers with no candidate get a hand-pasted URL, and where
// exact matches get accepted in bulk — all of which used to be a CSV in a
// spreadsheet.

// exactScoreFloor is where a suggestion counts as an exact title match. Scores
// arrive as 1.0 but have round-tripped through a SQLite REAL column.
const exactScoreFloor = 0.999

type linksPageData struct {
	ActiveProfile    models.Profile
	Sources          []models.Source
	SelectedSourceID int64
	// The scope selects reopen showing the last scan's slice: the queue's
	// point after a scan is reviewing what that scan covered, not everything
	// that has ever gone unlinked.
	SelectedStatus     string
	SelectedPrimary    string
	SelectedAlternates string
}

// linkScanScope is the parsed form of the scope controls: what slice of the
// library a scan (and the queue view) covers. Most sessions only need the
// series whose primary source is down or that still lack a working fallback,
// so scanning everything is the exception, not the default workflow.
type linkScanScope struct {
	Filter repository.LinkScanFilter
	// Show narrows the queue view only: "", "with" (only trackers with
	// candidates) or "without" (only trackers without).
	Show string
}

// linkStatusChoices are the tracker statuses the scope selector offers. The
// combined option exists because "find fallbacks for what I'm actually
// reading or about to" is the 90% case.
var linkStatusChoices = map[string][]string{
	"reading":      {"reading"},
	"plan_to_read": {"plan_to_read"},
	"reading+plan": {"reading", "plan_to_read"},
	"completed":    {"completed"},
	"on_hold":      {"on_hold"},
	"dropped":      {"dropped"},
}

func (h *DashboardHandler) parseLinkScanScope(c *fiber.Ctx) linkScanScope {
	value := func(name string) string {
		raw := strings.TrimSpace(c.FormValue(name))
		if raw == "" {
			raw = strings.TrimSpace(c.Query(name))
		}
		return strings.ToLower(raw)
	}

	scope := linkScanScope{}

	if statuses, ok := linkStatusChoices[value("status")]; ok {
		scope.Filter.Statuses = statuses
	}

	switch primary := value("primary"); primary {
	case "", "any":
	case "broken":
		// Resolved to the concrete set of failing sources at parse time. An
		// empty (non-nil) set deliberately matches nothing.
		scope.Filter.PrimarySourceIDs = h.unhealthySourceIDs(c.Context())
	default:
		if id, err := strconv.ParseInt(primary, 10, 64); err == nil && id > 0 {
			scope.Filter.PrimarySourceIDs = []int64{id}
		}
	}

	switch value("alternates") {
	case "0":
		zero := 0
		scope.Filter.MaxAlternates = &zero
	case "1":
		one := 1
		scope.Filter.MaxAlternates = &one
	}

	switch show := value("show"); show {
	case "with", "without":
		scope.Show = show
	}

	return scope
}

// unhealthySourceIDs health-checks every connector and returns the source ids
// of the ones that failed. The sweep is cached briefly: it fires a request at
// every site at once, which is not something a queue refresh should repeat.
func (h *DashboardHandler) unhealthySourceIDs(ctx context.Context) []int64 {
	h.sourceHealthMu.Lock()
	if time.Now().Before(h.sourceHealthExpires) && h.sourceHealthUnhealthy != nil {
		cached := append([]int64(nil), h.sourceHealthUnhealthy...)
		h.sourceHealthMu.Unlock()
		return cached
	}
	h.sourceHealthMu.Unlock()

	healthCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	statuses := h.registry.Health(healthCtx)

	unhealthyKeys := map[string]bool{}
	for _, status := range statuses {
		if !status.Healthy {
			unhealthyKeys[status.Key] = true
		}
	}

	ids := []int64{}
	if sources, err := h.scannableSources(); err == nil {
		for _, source := range sources {
			if unhealthyKeys[source.Key] {
				ids = append(ids, source.ID)
			}
		}
	}

	h.sourceHealthMu.Lock()
	h.sourceHealthUnhealthy = append([]int64(nil), ids...)
	h.sourceHealthExpires = time.Now().Add(5 * time.Minute)
	h.sourceHealthMu.Unlock()

	return ids
}

type linkSuggestionView struct {
	ID                 int64
	TrackerID          int64
	Title              string
	URL                string
	CoverURL           string
	LatestChapterLabel string
	ReleaseAgo         string
	ScoreLabel         string
	Exact              bool
}

type linkReviewTrackerView struct {
	ID                 int64
	SourceID           int64
	Title              string
	StatusLabel        string
	LatestChapterLabel string
	LatestReleaseAgo   string
	PrimaryURL         string
	PrimarySourceName  string
	// Error carries a failed manual-link attempt back onto the re-rendered
	// row instead of losing it to a dead end.
	Error string
}

type linkReviewGroupView struct {
	Tracker     linkReviewTrackerView
	Suggestions []linkSuggestionView
}

type linkQueueSummaryView struct {
	SourceID       int64
	SourceName     string
	PendingCount   int
	GroupCount     int
	NoMatchCount   int
	ExactCount     int
	ShowAcceptable bool
}

type linkQueueData struct {
	Summary      linkQueueSummaryView
	Groups       []linkReviewGroupView
	NoCandidates []linkReviewTrackerView
}

type linkScanStatusData struct {
	Running        bool
	SourceID       int64
	SourceName     string
	Total          int
	Done           int
	WithCandidates int
	Stopped        bool
	LastError      string
	JustFinished   bool
}

// linkCardResponseData renders one mutated review card (or its removal) plus
// an out-of-band refresh of the summary bar, so counters stay honest without
// re-rendering the whole queue on every click.
type linkCardResponseData struct {
	Group   *linkReviewGroupView
	NoMatch *linkReviewTrackerView
	Summary linkQueueSummaryView
}

func (h *DashboardHandler) LinksPage(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	sources, err := h.scannableSources()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	lastScope := h.lastLinkScanScopeValues()
	data := linksPageData{
		ActiveProfile:      *profile,
		Sources:            sources,
		SelectedSourceID:   h.selectedLinkSource(c, sources),
		SelectedStatus:     lastScope["status"],
		SelectedPrimary:    lastScope["primary"],
		SelectedAlternates: lastScope["alternates"],
	}
	if c.Query("source") == "" {
		if id, err := strconv.ParseInt(lastScope["source"], 10, 64); err == nil && id > 0 {
			data.SelectedSourceID = id
		}
	}
	return h.render(c, "links_page.html", data)
}

// rememberLinkScanScope keeps the raw scope values a scan launched with, and
// lastLinkScanScopeValues serves them back with defaults filled in.
func (h *DashboardHandler) rememberLinkScanScope(c *fiber.Ctx, sourceID int64) {
	scope := map[string]string{
		"source":     strconv.FormatInt(sourceID, 10),
		"status":     strings.ToLower(strings.TrimSpace(c.FormValue("status"))),
		"primary":    strings.ToLower(strings.TrimSpace(c.FormValue("primary"))),
		"alternates": strings.ToLower(strings.TrimSpace(c.FormValue("alternates"))),
	}
	h.lastLinkScanMu.Lock()
	h.lastLinkScanScope = scope
	h.lastLinkScanMu.Unlock()
}

func (h *DashboardHandler) lastLinkScanScopeValues() map[string]string {
	values := map[string]string{"source": "", "status": "all", "primary": "any", "alternates": "any"}
	h.lastLinkScanMu.Lock()
	defer h.lastLinkScanMu.Unlock()
	for key, value := range h.lastLinkScanScope {
		if strings.TrimSpace(value) != "" {
			values[key] = value
		}
	}
	return values
}

// scannableSources are the enabled sources that have a registered connector —
// the ones a scan can actually query.
func (h *DashboardHandler) scannableSources() ([]models.Source, error) {
	all, err := h.sourceRepo.ListEnabled()
	if err != nil {
		return nil, err
	}
	usable := make([]models.Source, 0, len(all))
	for _, source := range all {
		if _, ok := h.registry.Get(source.Key); ok {
			usable = append(usable, source)
		}
	}
	return usable, nil
}

// selectedLinkSource honours an explicit ?source= and otherwise defaults to
// WeebCentral, the source the review queue exists for right now.
func (h *DashboardHandler) selectedLinkSource(c *fiber.Ctx, sources []models.Source) int64 {
	if raw := strings.TrimSpace(c.Query("source")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			return id
		}
	}
	for _, source := range sources {
		if source.Key == "weebcentral" {
			return source.ID
		}
	}
	if len(sources) > 0 {
		return sources[0].ID
	}
	return 0
}

func (h *DashboardHandler) LinksQueuePartial(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	data, err := h.buildLinkQueue(profile.ID, source, h.parseLinkScanScope(c))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return h.render(c, "links_queue_partial.html", data)
}

func (h *DashboardHandler) linkSourceFromRequest(c *fiber.Ctx) (*models.Source, error) {
	raw := strings.TrimSpace(c.Query("source"))
	if raw == "" {
		raw = strings.TrimSpace(c.FormValue("source"))
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, fiber.NewError(fiber.StatusBadRequest, "source is required")
	}
	source, err := h.sourceRepo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "unknown source")
	}
	return source, nil
}

func (h *DashboardHandler) buildLinkQueue(profileID int64, source *models.Source, scope linkScanScope) (linkQueueData, error) {
	queue, err := h.linkSuggestionRepo.ListReviewQueue(profileID, source.ID, scope.Filter)
	if err != nil {
		return linkQueueData{}, err
	}

	data := linkQueueData{
		Summary: linkQueueSummaryView{SourceID: source.ID, SourceName: source.Name},
	}
	for _, item := range queue {
		trackerView := h.toLinkReviewTrackerView(item, source.ID)
		if len(item.Suggestions) == 0 {
			data.NoCandidates = append(data.NoCandidates, trackerView)
			data.Summary.NoMatchCount++
			continue
		}

		group := linkReviewGroupView{Tracker: trackerView}
		for _, suggestion := range item.Suggestions {
			group.Suggestions = append(group.Suggestions, toLinkSuggestionView(suggestion))
			data.Summary.PendingCount++
		}
		if isBulkAcceptable(item.Suggestions) {
			data.Summary.ExactCount++
		}
		data.Groups = append(data.Groups, group)
		data.Summary.GroupCount++
	}
	data.Summary.ShowAcceptable = data.Summary.ExactCount > 0

	// The view filter hides a section without touching the summary counts:
	// the numbers keep describing everything in scope.
	switch scope.Show {
	case "with":
		data.NoCandidates = nil
	case "without":
		data.Groups = nil
	}

	return data, nil
}

// isBulkAcceptable marks a tracker whose best candidate is an unambiguous
// exact match: exactly one pending candidate at the exact score. Two exact
// candidates (a series and its reprint under the same normalized title) stay
// in the manual queue.
func isBulkAcceptable(suggestions []repository.LinkSuggestion) bool {
	exact := 0
	for _, suggestion := range suggestions {
		if suggestion.Score >= exactScoreFloor {
			exact++
		}
	}
	return exact == 1
}

func (h *DashboardHandler) toLinkReviewTrackerView(item repository.LinkReviewTracker, sourceID int64) linkReviewTrackerView {
	view := linkReviewTrackerView{
		ID:          item.TrackerID,
		SourceID:    sourceID,
		Title:       item.Title,
		StatusLabel: statusLabel(item.Status),
		PrimaryURL:  item.SourceURL,
	}
	if item.LatestKnownChapter != nil {
		view.LatestChapterLabel = formatChapterLabel(*item.LatestKnownChapter)
	}
	if item.LatestReleaseAt != nil {
		view.LatestReleaseAgo = relativeTime(*item.LatestReleaseAt)
	}
	if key := inferSourceKeyFromURL(item.SourceURL); key != "" {
		if connector, ok := h.registry.Get(key); ok {
			view.PrimarySourceName = connector.Name()
		}
	}
	return view
}

func toLinkSuggestionView(suggestion repository.LinkSuggestion) linkSuggestionView {
	view := linkSuggestionView{
		ID:        suggestion.ID,
		TrackerID: suggestion.TrackerID,
		Title:     suggestion.CandidateTitle,
		URL:       suggestion.CandidateURL,
		Exact:     suggestion.Score >= exactScoreFloor,
	}
	if suggestion.CandidateCoverURL != nil {
		view.CoverURL = *suggestion.CandidateCoverURL
	}
	if suggestion.CandidateLatestChapter != nil {
		view.LatestChapterLabel = formatChapterLabel(*suggestion.CandidateLatestChapter)
	}
	if suggestion.CandidateReleaseAt != nil {
		view.ReleaseAgo = relativeTime(*suggestion.CandidateReleaseAt)
	}
	if view.Exact {
		view.ScoreLabel = "Exact title"
	} else {
		view.ScoreLabel = strconv.Itoa(int(suggestion.Score*100+0.5)) + "% match"
	}
	return view
}

func (h *DashboardHandler) StartLinkScan(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	if err := h.linkScanner.Start(profile.ID, source.ID, source.Key, source.Name, h.parseLinkScanScope(c).Filter); err != nil {
		// An already-running scan is not a failure worth a dead end: show the
		// live status instead.
		return h.renderLinkScanStatus(c, false)
	}
	h.rememberLinkScanScope(c, source.ID)
	return h.renderLinkScanStatus(c, false)
}

// StopLinkScan winds down a running scan after the tracker it is on; what it
// already stored stands.
func (h *DashboardHandler) StopLinkScan(c *fiber.Ctx) error {
	h.linkScanner.Stop()
	return h.renderLinkScanStatus(c, false)
}

func (h *DashboardHandler) LinkScanStatus(c *fiber.Ctx) error {
	return h.renderLinkScanStatus(c, true)
}

func (h *DashboardHandler) renderLinkScanStatus(c *fiber.Ctx, fromPoll bool) error {
	progress := h.linkScanner.Snapshot()
	data := linkScanStatusData{
		Running:        progress.Running,
		SourceID:       progress.SourceID,
		SourceName:     progress.SourceName,
		Total:          progress.Total,
		Done:           progress.Done,
		WithCandidates: progress.WithCandidates,
		Stopped:        progress.Stopped,
		LastError:      progress.LastError,
		// A poll that finds the scan freshly finished is the transition edge:
		// that render carries the one-shot queue refresh. The recency window
		// keeps a later page load from re-triggering it.
		JustFinished: fromPoll && !progress.Running && progress.FinishedAt != nil &&
			time.Since(*progress.FinishedAt) < 10*time.Second,
	}
	return h.render(c, "links_scan_status_partial.html", data)
}

func (h *DashboardHandler) AcceptLinkSuggestion(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	suggestionID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || suggestionID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid suggestion id")
	}

	suggestion, err := h.linkSuggestionRepo.GetPendingSuggestion(profile.ID, suggestionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	if suggestion == nil {
		return c.Status(fiber.StatusNotFound).SendString("suggestion not found")
	}

	if err := h.acceptSuggestion(profile.ID, *suggestion); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	return h.renderLinkCardResponse(c, profile.ID, suggestion.SourceID, suggestion.TrackerID, "")
}

func (h *DashboardHandler) acceptSuggestion(profileID int64, suggestion repository.LinkSuggestion) error {
	if err := h.trackerRepo.UpsertTrackerSource(profileID, suggestion.TrackerID, models.TrackerSource{
		SourceID:     suggestion.SourceID,
		SourceItemID: suggestion.CandidateItemID,
		SourceURL:    suggestion.CandidateURL,
	}); err != nil {
		return err
	}
	if err := h.linkSuggestionRepo.DecideSuggestion(profileID, suggestion.ID, repository.LinkSuggestionAccepted); err != nil {
		return err
	}
	return h.linkSuggestionRepo.RejectPendingSiblings(suggestion.TrackerID, suggestion.SourceID, suggestion.ID)
}

func (h *DashboardHandler) RejectLinkSuggestion(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	suggestionID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || suggestionID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid suggestion id")
	}

	suggestion, err := h.linkSuggestionRepo.GetPendingSuggestion(profile.ID, suggestionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	if suggestion == nil {
		return c.Status(fiber.StatusNotFound).SendString("suggestion not found")
	}

	if err := h.linkSuggestionRepo.DecideSuggestion(profile.ID, suggestionID, repository.LinkSuggestionRejected); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	return h.renderLinkCardResponse(c, profile.ID, suggestion.SourceID, suggestion.TrackerID, "")
}

func (h *DashboardHandler) DismissLinkTracker(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	trackerID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || trackerID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid tracker id")
	}

	if err := h.linkSuggestionRepo.DismissTracker(profile.ID, trackerID, source.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, "")
}

// ManualLinkTracker links a hand-pasted URL — the last resort for titles the
// scan matched wrong or not at all. The URL may belong to any supported site,
// not just the source under review: what the user is saying is "this is where
// this series lives", and once any alternate is linked the tracker leaves this
// source's queue.
func (h *DashboardHandler) ManualLinkTracker(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	trackerID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || trackerID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid tracker id")
	}

	rawURL := strings.TrimSpace(c.FormValue("url"))
	if rawURL == "" {
		return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, "Paste a series URL first.")
	}

	linkedSource, err := h.attachSourceByURL(c.Context(), profile.ID, trackerID, rawURL)
	if err != nil {
		return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, err.Error())
	}

	// Linking a different site still settles this tracker's review: mark it
	// dismissed for the source under review so it leaves the queue, and drop
	// whatever candidates were pending.
	if linkedSource.ID != source.ID {
		if err := h.linkSuggestionRepo.DismissTracker(profile.ID, trackerID, source.ID); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
	} else {
		if err := h.linkSuggestionRepo.RejectPendingSiblings(trackerID, source.ID, 0); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
	}

	return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, "")
}

// attachSourceByURL links a pasted series URL to a tracker. The URL's host
// must map to a registered connector and source; verification through the
// connector is best-effort, not a gate — the sites most worth linking by hand
// are exactly the ones this server cannot reach (MangaFire behind its browser
// challenge) while the reader's own browser can. An unverified URL is linked
// as pasted and canonicalized whenever the site answers again.
func (h *DashboardHandler) attachSourceByURL(parent context.Context, profileID int64, trackerID int64, rawURL string) (*models.Source, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("a series URL is required")
	}

	connector, ok := h.registry.Get(trimmed)
	if !ok {
		return nil, fmt.Errorf("that site has no connector; the poller could not read it")
	}
	linkedSource, err := h.sourceRepo.GetByKey(connector.Key())
	if err != nil {
		return nil, err
	}
	if linkedSource == nil {
		return nil, fmt.Errorf("that site is not registered as a source")
	}

	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	canonicalURL := trimmed
	var itemID *string
	if resolved, resolveErr := connector.ResolveByURL(ctx, trimmed); resolveErr == nil && resolved != nil {
		if resolvedURL := strings.TrimSpace(resolved.URL); resolvedURL != "" {
			canonicalURL = resolvedURL
		}
		if resolvedItemID := strings.TrimSpace(resolved.SourceItemID); resolvedItemID != "" {
			itemID = &resolvedItemID
		}
	}

	if err := h.trackerRepo.UpsertTrackerSource(profileID, trackerID, models.TrackerSource{
		SourceID:     linkedSource.ID,
		SourceItemID: itemID,
		SourceURL:    canonicalURL,
	}); err != nil {
		return nil, err
	}
	return linkedSource, nil
}

// AcceptExactLinkMatches bulk-accepts every tracker whose single best pending
// candidate is an unambiguous exact title match.
func (h *DashboardHandler) AcceptExactLinkMatches(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	scope := h.parseLinkScanScope(c)
	queue, err := h.linkSuggestionRepo.ListReviewQueue(profile.ID, source.ID, scope.Filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	for _, item := range queue {
		if !isBulkAcceptable(item.Suggestions) {
			continue
		}
		for _, suggestion := range item.Suggestions {
			if suggestion.Score >= exactScoreFloor {
				if err := h.acceptSuggestion(profile.ID, suggestion); err != nil {
					return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
				}
				break
			}
		}
	}

	data, err := h.buildLinkQueue(profile.ID, source, scope)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return h.render(c, "links_queue_partial.html", data)
}

// renderLinkCardResponse re-renders one tracker's review card after a
// mutation — or renders its removal when the tracker left the queue — plus an
// out-of-band summary refresh so the counters stay honest.
func (h *DashboardHandler) renderLinkCardResponse(c *fiber.Ctx, profileID int64, sourceID int64, trackerID int64, errorMessage string) error {
	source, err := h.sourceRepo.GetByID(sourceID)
	if err != nil || source == nil {
		return c.Status(fiber.StatusInternalServerError).SendString("source lookup failed")
	}

	// The action buttons include the scope controls, so the re-rendered
	// summary keeps counting the same slice the queue is showing.
	data, err := h.buildLinkQueue(profileID, source, h.parseLinkScanScope(c))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	response := linkCardResponseData{Summary: data.Summary}
	for index := range data.Groups {
		if data.Groups[index].Tracker.ID == trackerID {
			data.Groups[index].Tracker.Error = errorMessage
			response.Group = &data.Groups[index]
			break
		}
	}
	if response.Group == nil {
		for index := range data.NoCandidates {
			if data.NoCandidates[index].ID == trackerID {
				data.NoCandidates[index].Error = errorMessage
				response.NoMatch = &data.NoCandidates[index]
				break
			}
		}
	}
	// When neither is set the tracker left the queue and the card renders to
	// nothing, which is exactly the removal the swap needs.

	return h.render(c, "links_card_response.html", response)
}
