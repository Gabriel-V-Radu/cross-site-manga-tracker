package handlers

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gofiber/fiber/v2"
)

// The source health panel answers "is any site failing?" from what the poller
// actually saw (connectors.SourceHealthBoard) plus the breakers' live state.
// Both handlers read memory only — no query, no request to any site — so the
// toolbar indicator can refresh itself every minute for free.

type sourceHealthIndicatorView struct {
	// Tone picks the colour: ok, degraded, failing or unknown.
	Tone  string
	Label string
	// Title is the hover text: what the label summarises.
	Title string
}

type sourceHealthRowView struct {
	Name       string
	Tone       string
	StateLabel string
	Reason     string
	LastError  string
	Facts      []string
	Counts     string
	Coverage   string
	// CycleWord says which cycle the counts describe: "last cycle", or
	// "this cycle so far" while the verdict is provisional.
	CycleWord string
	// Unchecked lists the active profile's trackers this source left
	// unchecked last cycle; UncheckedMore counts the rest (other profile, or
	// past the list cap).
	Unchecked     []string
	UncheckedMore int
}

type sourceHealthOrphanView struct {
	Key      string
	Count    int
	Trackers []string
}

type sourceHealthModalData struct {
	Indicator sourceHealthIndicatorView
	CycleLine string
	Notice    string
	Rows      []sourceHealthRowView
	Orphans   []sourceHealthOrphanView
}

// SourceHealthIndicator renders the toolbar pill, which re-requests itself.
func (h *DashboardHandler) SourceHealthIndicator(c *fiber.Ctx) error {
	now := time.Now().UTC()
	return h.render(c, "source_health_indicator.html", buildSourceHealthIndicator(h.registry.SourceHealthReport(now), now))
}

// SourceHealthModal renders the per-source detail. The profile only narrows
// which tracker titles are listed: sources are shared by both libraries and
// the counts cover every profile.
func (h *DashboardHandler) SourceHealthModal(c *fiber.Ctx) error {
	activeProfile, err := h.profileResolver.Resolve(c)
	if err != nil {
		return h.failProfile(c, err)
	}
	now := time.Now().UTC()
	return h.render(c, "source_health_modal.html", buildSourceHealthModal(h.registry.SourceHealthReport(now), activeProfile.ID, now))
}

// pollerStalled reports a poller that has not finished a cycle for far longer
// than its rest interval and is not in one either — a wedged loop, which would
// otherwise leave the last verdicts on screen looking current.
func pollerStalled(report connectors.SourceHealthReport, now time.Time) bool {
	if !report.PollerStarted || report.CycleRunning || report.Interval <= 0 || report.LastCycleFinishedAt.IsZero() {
		return false
	}
	return now.Sub(report.LastCycleFinishedAt) > 3*report.Interval+time.Hour
}

func buildSourceHealthIndicator(report connectors.SourceHealthReport, now time.Time) sourceHealthIndicatorView {
	failing := report.Count(connectors.SourceStateFailing)
	degraded := report.Count(connectors.SourceStateDegraded)
	orphaned := 0
	for _, orphan := range report.Orphans {
		orphaned += orphan.Count
	}

	switch {
	case failing > 0:
		title := "Sites failing in the last poll cycle"
		if degraded > 0 {
			title += fmt.Sprintf("; %d more with problems", degraded)
		}
		return sourceHealthIndicatorView{Tone: "failing", Label: countedNoun(failing, "source") + " failing", Title: title}
	case degraded > 0:
		return sourceHealthIndicatorView{Tone: "degraded", Label: countedNoun(degraded, "source") + " with problems", Title: "Some requests to these sites failed or are cooling down"}
	case report.LastCycleError != "":
		return sourceHealthIndicatorView{Tone: "degraded", Label: "Poll cycle failed", Title: "The last poll cycle could not load the trackers"}
	case pollerStalled(report, now):
		return sourceHealthIndicatorView{Tone: "degraded", Label: "Poller stalled", Title: "No poll cycle has finished for " + shortDuration(now.Sub(report.LastCycleFinishedAt))}
	case orphaned > 0:
		return sourceHealthIndicatorView{Tone: "degraded", Label: countedNoun(orphaned, "tracker") + " not polled", Title: "Their primary site has no connector any more"}
	case !report.PollerStarted:
		return sourceHealthIndicatorView{Tone: "unknown", Label: "Polling off", Title: "This instance does not poll, so there are no results to judge the sites by"}
	case report.LastCycleFinishedAt.IsZero():
		return sourceHealthIndicatorView{Tone: "unknown", Label: "Checking sources", Title: "The first poll cycle since the server started is still running"}
	default:
		return sourceHealthIndicatorView{Tone: "ok", Label: "Sources OK", Title: "Every site answered the last poll cycle"}
	}
}

func buildSourceHealthModal(report connectors.SourceHealthReport, profileID int64, now time.Time) sourceHealthModalData {
	data := sourceHealthModalData{Indicator: buildSourceHealthIndicator(report, now)}

	switch {
	case !report.PollerStarted:
		data.Notice = "Polling is off in this instance, so no site has been checked. Any cooldown below comes from requests the dashboard made."
	case report.CycleRunning:
		data.CycleLine = "Poll cycle in progress, started " + relativeTime(report.CycleStartedAt) + "."
		if !report.LastCycleFinishedAt.IsZero() {
			data.CycleLine += " The previous one finished " + relativeTime(report.LastCycleFinishedAt) + "."
		}
		data.CycleLine += " Sites this cycle has already asked show what it has seen so far."
	case !report.LastCycleFinishedAt.IsZero():
		data.CycleLine = fmt.Sprintf("Last poll cycle finished %s (took %s).",
			relativeTime(report.LastCycleFinishedAt),
			shortDuration(report.LastCycleFinishedAt.Sub(report.LastCycleStartedAt)))
		if next := report.LastCycleFinishedAt.Add(report.Interval); report.Interval > 0 && next.After(now) {
			data.CycleLine += " Next one in about " + shortDuration(next.Sub(now)) + "."
		}
	}
	if report.LastCycleError != "" {
		data.Notice = "The last poll cycle failed before asking any site: " + report.LastCycleError
	} else if pollerStalled(report, now) {
		data.Notice = "No poll cycle has finished for " + shortDuration(now.Sub(report.LastCycleFinishedAt)) + "; the verdicts below may be stale. Check the container log."
	}

	for _, source := range report.Sources {
		data.Rows = append(data.Rows, buildSourceHealthRow(source, profileID))
	}
	// Worst first, so a failing site is the first thing in the list.
	sort.SliceStable(data.Rows, func(i, j int) bool {
		return sourceToneRank(data.Rows[i].Tone) < sourceToneRank(data.Rows[j].Tone)
	})

	for _, orphan := range report.Orphans {
		view := sourceHealthOrphanView{Key: orphan.Key, Count: orphan.Count}
		for _, tracker := range orphan.Trackers {
			if tracker.ProfileID == profileID {
				view.Trackers = append(view.Trackers, tracker.Title)
			}
		}
		data.Orphans = append(data.Orphans, view)
	}
	return data
}

func buildSourceHealthRow(source connectors.SourceHealth, profileID int64) sourceHealthRowView {
	row := sourceHealthRowView{
		Name:      source.Name,
		Tone:      string(source.State),
		Reason:    source.Reason,
		LastError: source.LastError,
	}
	switch source.State {
	case connectors.SourceStateFailing:
		row.StateLabel = "Failing"
	case connectors.SourceStateDegraded:
		row.StateLabel = "Problems"
	case connectors.SourceStateOK:
		row.StateLabel = "OK"
		// An old error on a healthy site is history, not a clue.
		row.LastError = ""
	default:
		row.StateLabel = "No data"
		row.Tone = "unknown"
	}

	if !source.Since.IsZero() {
		row.Facts = append(row.Facts, fmt.Sprintf("Nothing got through since %s (%s in a row)", relativeTime(source.Since), countedNoun(source.BadCycles, "cycle")))
	}
	if source.CooldownLeft > 0 {
		row.Facts = append(row.Facts, fmt.Sprintf("Cooling down for %s more: %s", shortDuration(source.CooldownLeft), source.CooldownReason))
	}
	if !source.LastSuccessAt.IsZero() {
		row.Facts = append(row.Facts, "Last answer "+relativeTime(source.LastSuccessAt))
	} else if !source.LastAskedAt.IsZero() {
		row.Facts = append(row.Facts, "No answer since the server started")
	}
	if source.State == connectors.SourceStateUnknown && source.CooldownLeft <= 0 && source.LastAskedAt.IsZero() {
		row.Facts = append(row.Facts, "Not asked by the poller since the server started")
	}

	row.CycleWord = "last cycle"
	if source.Provisional {
		row.CycleWord = "this cycle so far"
	}

	var counts []string
	if source.Answered > 0 {
		counts = append(counts, fmt.Sprintf("%d answered", source.Answered))
	}
	if source.Failed > 0 {
		counts = append(counts, fmt.Sprintf("%d failed", source.Failed))
	}
	if source.Refused > 0 {
		counts = append(counts, fmt.Sprintf("%d skipped while cooling down", source.Refused))
	}
	if source.NoChapter > 0 {
		counts = append(counts, fmt.Sprintf("%d without a chapter", source.NoChapter))
	}
	if source.NotFound > 0 {
		counts = append(counts, fmt.Sprintf("%d not found", source.NotFound))
	}
	if source.BadLinks > 0 {
		counts = append(counts, countedNoun(source.BadLinks, "unusable link"))
	}
	if source.AsFallback > 0 {
		counts = append(counts, fmt.Sprintf("%d as a fallback", source.AsFallback))
	}
	if len(counts) > 0 {
		row.Counts = strings.ToUpper(row.CycleWord[:1]) + row.CycleWord[1:] + ": " + strings.Join(counts, " · ")
	}

	switch {
	case source.Covered > 0 && source.UncheckedCount > 0:
		row.Coverage = fmt.Sprintf("%s fell back to another site; %d could not be checked at all.", countedNoun(source.Covered, "tracker"), source.UncheckedCount)
	case source.Covered > 0:
		row.Coverage = countedNoun(source.Covered, "tracker") + " fell back to another site and kept updating."
	case source.UncheckedCount > 0:
		row.Coverage = countedNoun(source.UncheckedCount, "tracker") + " could not be checked at all."
	}
	for _, tracker := range source.Unchecked {
		if tracker.ProfileID == profileID {
			row.Unchecked = append(row.Unchecked, tracker.Title)
		}
	}
	row.UncheckedMore = source.UncheckedCount - len(row.Unchecked)
	return row
}

func sourceToneRank(tone string) int {
	switch tone {
	case "failing":
		return 0
	case "degraded":
		return 1
	case "ok":
		return 2
	default:
		return 3
	}
}

// countedNoun spells "1 source" / "3 sources".
func countedNoun(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// shortDuration renders a span the way the panel talks about cooldowns and
// cycles: "45s", "12 min", "3 h 5 min".
func shortDuration(span time.Duration) string {
	if span < time.Minute {
		seconds := int(span.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := int(span.Round(time.Minute) / time.Minute)
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	hours, minutes := minutes/60, minutes%60
	if minutes == 0 {
		return fmt.Sprintf("%d h", hours)
	}
	return fmt.Sprintf("%d h %d min", hours, minutes)
}
