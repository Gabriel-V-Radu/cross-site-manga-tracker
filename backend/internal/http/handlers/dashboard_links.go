package handlers

import (
	"context"
	"fmt"
	"log/slog"
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
	if sources, err := h.scannableSources(ctx); err == nil {
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
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	sources, err := h.scannableSources(c.Context())
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load sources", err)
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
	// Cloned for the same reason the page gate clones: a form value borrows the
	// request's body buffer, ToLower returns the input unchanged when it is
	// already lowercase, and this map is kept on the handler until the next
	// scan. Without the copy the remembered scope can turn into the bytes of
	// whatever request reused the buffer.
	scope := map[string]string{
		"source":     strconv.FormatInt(sourceID, 10),
		"status":     strings.Clone(strings.ToLower(strings.TrimSpace(c.FormValue("status")))),
		"primary":    strings.Clone(strings.ToLower(strings.TrimSpace(c.FormValue("primary")))),
		"alternates": strings.Clone(strings.ToLower(strings.TrimSpace(c.FormValue("alternates")))),
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
func (h *DashboardHandler) scannableSources(ctx context.Context) ([]models.Source, error) {
	all, err := h.sourceRepo.ListEnabled(ctx)
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
// MangaHub, the source the review queue exists for right now.
func (h *DashboardHandler) selectedLinkSource(c *fiber.Ctx, sources []models.Source) int64 {
	if raw := strings.TrimSpace(c.Query("source")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			return id
		}
	}
	for _, source := range sources {
		if source.Key == "mangahub" {
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
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, publicRequestMessage(err, "Could not load that site"), err)
	}

	data, err := h.buildLinkQueue(c.Context(), profile.ID, source, h.parseLinkScanScope(c))
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load the review queue", err)
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
	source, err := h.sourceRepo.GetByID(c.Context(), id)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "unknown source")
	}
	return source, nil
}

func (h *DashboardHandler) buildLinkQueue(ctx context.Context, profileID int64, source *models.Source, scope linkScanScope) (linkQueueData, error) {
	queue, err := h.linkSuggestionRepo.ListReviewQueue(ctx, profileID, source.ID, scope.Filter)
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
	if connector, ok := h.connectorForURL(item.SourceURL); ok {
		view.PrimarySourceName = connector.Name()
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
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, publicRequestMessage(err, "Could not load that site"), err)
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
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	suggestionID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || suggestionID <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "invalid suggestion id", err)
	}

	// The whole decision — link row, accepted status, sibling rejections —
	// happens in one repository transaction: a half-applied decision leaves
	// the queue offering a candidate for a tracker it has already linked.
	accepted, err := h.linkSuggestionRepo.AcceptSuggestion(c.Context(), profile.ID, suggestionID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to accept the suggestion", err)
	}
	if accepted == nil {
		return h.fail(c, fiber.StatusNotFound, "suggestion not found", nil)
	}
	h.invalidateLinkLookups()

	return h.renderLinkCardResponse(c, profile.ID, accepted.SourceID, accepted.TrackerID, "")
}

func (h *DashboardHandler) RejectLinkSuggestion(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}

	suggestionID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || suggestionID <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "invalid suggestion id", err)
	}

	suggestion, err := h.linkSuggestionRepo.GetPendingSuggestion(c.Context(), profile.ID, suggestionID)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load the suggestion", err)
	}
	if suggestion == nil {
		return h.fail(c, fiber.StatusNotFound, "suggestion not found", nil)
	}

	if err := h.linkSuggestionRepo.DecideSuggestion(c.Context(), profile.ID, suggestionID, repository.LinkSuggestionRejected); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to reject the suggestion", err)
	}

	return h.renderLinkCardResponse(c, profile.ID, suggestion.SourceID, suggestion.TrackerID, "")
}

func (h *DashboardHandler) DismissLinkTracker(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, publicRequestMessage(err, "Could not load that site"), err)
	}

	trackerID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || trackerID <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "invalid tracker id", err)
	}

	if err := h.linkSuggestionRepo.DismissTracker(c.Context(), profile.ID, trackerID, source.ID); err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to dismiss the tracker", err)
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
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, publicRequestMessage(err, "Could not load that site"), err)
	}

	trackerID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || trackerID <= 0 {
		return h.fail(c, fiber.StatusBadRequest, "invalid tracker id", err)
	}

	rawURL := strings.TrimSpace(c.FormValue("url"))
	if rawURL == "" {
		return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, "Paste a series URL first.")
	}

	_, link, err := h.resolveSourceLink(c.Context(), rawURL)
	if err != nil {
		return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, err.Error())
	}

	// Linking a different site still settles this tracker's review: the
	// repository marks it dismissed for the source under review so it leaves
	// the queue, and drops whatever candidates were pending — in the same
	// transaction as the link, so the queue can never end up holding a
	// settled tracker's live candidates.
	if err := h.linkSuggestionRepo.ApplyManualLink(c.Context(), profile.ID, trackerID, link, source.ID); err != nil {
		// Reported inside the re-rendered row rather than as a status, so the
		// user can retry the paste; the repository's own wording names tables
		// and belongs in the log instead of the card.
		slog.Error("manual link failed", "path", c.Path(), "tracker", trackerID, "source", source.ID, "error", err)
		return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, "Could not save that link.")
	}
	h.invalidateLinkLookups()

	return h.renderLinkCardResponse(c, profile.ID, source.ID, trackerID, "")
}

// resolveSourceLink turns a pasted series URL into the link to store. The
// URL's host must map to a registered connector and source; verification
// through the connector is best-effort, not a gate — the sites most worth
// linking by hand are exactly the ones this server cannot reach (MangaFire
// behind its browser challenge) while the reader's own browser can. An
// unverified URL is linked as pasted and canonicalized whenever the site
// answers again.
//
// It writes nothing: the connector call can take the full timeout, and with a
// single SQLite connection no transaction may be open across it.
func (h *DashboardHandler) resolveSourceLink(parent context.Context, rawURL string) (*models.Source, models.TrackerSource, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, models.TrackerSource{}, fmt.Errorf("a series URL is required")
	}

	connector, ok := h.registry.Get(trimmed)
	if !ok {
		return nil, models.TrackerSource{}, fmt.Errorf("that site has no connector; the poller could not read it")
	}
	linkedSource, err := h.sourceRepo.GetByKey(parent, connector.Key())
	if err != nil {
		// Both callers show this text to the user — in the review card and in
		// the edit form's error — so the lookup's own wording stays in the log.
		slog.Error("source lookup failed", "source", connector.Key(), "error", err)
		return nil, models.TrackerSource{}, fmt.Errorf("that site could not be checked; try again")
	}
	if linkedSource == nil {
		return nil, models.TrackerSource{}, fmt.Errorf("that site is not registered as a source")
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

	return linkedSource, models.TrackerSource{
		SourceID:     linkedSource.ID,
		SourceItemID: itemID,
		SourceURL:    canonicalURL,
	}, nil
}

// AcceptExactLinkMatches bulk-accepts every tracker whose single best pending
// candidate is an unambiguous exact title match.
func (h *DashboardHandler) AcceptExactLinkMatches(c *fiber.Ctx) error {
	profile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, "Invalid profile", err)
	}
	source, err := h.linkSourceFromRequest(c)
	if err != nil {
		return h.fail(c, fiber.StatusBadRequest, publicRequestMessage(err, "Could not load that site"), err)
	}

	scope := h.parseLinkScanScope(c)
	queue, err := h.linkSuggestionRepo.ListReviewQueue(c.Context(), profile.ID, source.ID, scope.Filter)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load the review queue", err)
	}

	linked := false
	for _, item := range queue {
		if !isBulkAcceptable(item.Suggestions) {
			continue
		}
		for _, suggestion := range item.Suggestions {
			if suggestion.Score >= exactScoreFloor {
				// A nil result means the candidate stopped being pending
				// between listing the queue and deciding it; nothing to undo,
				// each accept is its own transaction.
				accepted, err := h.linkSuggestionRepo.AcceptSuggestion(c.Context(), profile.ID, suggestion.ID)
				if err != nil {
					return h.fail(c, fiber.StatusInternalServerError, "Failed to accept the exact matches", err)
				}
				linked = linked || accepted != nil
				break
			}
		}
	}
	if linked {
		h.invalidateLinkLookups()
	}

	data, err := h.buildLinkQueue(c.Context(), profile.ID, source, scope)
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load the review queue", err)
	}
	return h.render(c, "links_queue_partial.html", data)
}

// renderLinkCardResponse re-renders one tracker's review card after a
// mutation — or renders its removal when the tracker left the queue — plus an
// out-of-band summary refresh so the counters stay honest.
func (h *DashboardHandler) renderLinkCardResponse(c *fiber.Ctx, profileID int64, sourceID int64, trackerID int64, errorMessage string) error {
	source, err := h.sourceRepo.GetByID(c.Context(), sourceID)
	if err != nil || source == nil {
		return h.fail(c, fiber.StatusInternalServerError, "source lookup failed", err)
	}

	// The action buttons include the scope controls, so the re-rendered
	// summary keeps counting the same slice the queue is showing.
	data, err := h.buildLinkQueue(c.Context(), profileID, source, h.parseLinkScanScope(c))
	if err != nil {
		return h.fail(c, fiber.StatusInternalServerError, "Failed to load the review queue", err)
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
