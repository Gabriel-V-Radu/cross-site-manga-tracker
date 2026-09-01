// Command repair-latest-chapter sets a tracker's latest_known_chapter to a
// value verified by hand. It exists for the aftermath of a poisoned number: a
// source that reported junk (a year as a chapter, a number lifted from the
// series title, a placeholder entry) got it stored by the highest-number-wins
// poll reconciliation, and once stored the same source re-confirms it every
// cycle, so the pending-lower path can never walk it back on its own.
//
// Each assignment prints the value it replaces. The pending-lower state is
// cleared (a manual repair supersedes whatever correction was aging toward
// confirmation), latest_chapter_seen_at falls back to the release date or the
// tracker's creation (the stored timestamp belonged to the junk number's
// arrival, not a real release), and the reporting source is cleared.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/app"
)

func main() {
	var (
		assign = flag.String("assign", "", `Comma-separated "trackerID=chapter" pairs, e.g. "28=271,167=199"`)
		dryRun = flag.Bool("dry-run", false, "Preview changes without writing to DB")
	)
	flag.Parse()

	assignments, err := parseAssignments(*assign)
	if err != nil {
		slog.Error("invalid -assign", "error", err)
		os.Exit(1)
	}
	if len(assignments) == 0 {
		slog.Error("-assign is required")
		os.Exit(1)
	}

	// Migrations only when the run is going to write: a dry run from a newer
	// checkout than the running image must not migrate the live database out
	// from under it (the same rule cleanup-stale-sources follows).
	level := slog.LevelInfo
	runtime, err := app.Open(app.Options{Migrate: !*dryRun, LogLevel: &level})
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer runtime.Close()

	repaired, err := runRepairs(runtime.DB, assignments, *dryRun)
	if err != nil {
		slog.Error("repair aborted", "error", err)
		os.Exit(1)
	}
	slog.Info("repair completed", "dry_run", *dryRun, "requested", len(assignments), "repaired", repaired)
}

type assignment struct {
	TrackerID int64
	Chapter   float64
}

func parseAssignments(raw string) ([]assignment, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	seen := map[int64]struct{}{}
	assignments := make([]assignment, 0)
	for _, pair := range strings.Split(trimmed, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%q is not of the form trackerID=chapter", pair)
		}
		id, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q has no valid tracker id", pair)
		}
		chapter, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || chapter < 0 {
			return nil, fmt.Errorf("%q has no valid chapter number", pair)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("tracker %d is assigned twice", id)
		}
		seen[id] = struct{}{}
		assignments = append(assignments, assignment{TrackerID: id, Chapter: chapter})
	}

	sort.Slice(assignments, func(i, j int) bool { return assignments[i].TrackerID < assignments[j].TrackerID })
	return assignments, nil
}

func runRepairs(db *sql.DB, assignments []assignment, dryRun bool) (int, error) {
	repaired := 0
	for _, item := range assignments {
		var (
			title    string
			stored   sql.NullFloat64
			lastRead sql.NullFloat64
		)
		err := db.QueryRow(`
			SELECT title, latest_known_chapter, last_read_chapter
			FROM trackers WHERE id = ?
		`, item.TrackerID).Scan(&title, &stored, &lastRead)
		if err == sql.ErrNoRows {
			return repaired, fmt.Errorf("tracker %d does not exist", item.TrackerID)
		}
		if err != nil {
			return repaired, fmt.Errorf("read tracker %d: %w", item.TrackerID, err)
		}

		target := item.Chapter
		// Same rule as the poller: no correction may make read chapters look
		// unread.
		if lastRead.Valid && target < lastRead.Float64 {
			slog.Warn("target is below the read position; clamping",
				"tracker_id", item.TrackerID, "target", target, "last_read", lastRead.Float64)
			target = lastRead.Float64
		}

		slog.Info("repairing latest chapter",
			"tracker_id", item.TrackerID,
			"title", title,
			"from", storedLabel(stored),
			"to", target)

		if dryRun {
			repaired++
			continue
		}

		// latest_chapter_seen_at takes the rule migration 0018 backfilled with,
		// not NULL: the next poll fills a NULL with its own time (UpdatePollingState
		// COALESCEs it), which ranks the repaired tracker at the top of the
		// default sort as if the chapter had just appeared. The source that
		// reported the junk number is dropped too — the number it is attributed
		// to no longer exists.
		if _, err := db.Exec(`
			UPDATE trackers
			SET latest_known_chapter = ?,
				latest_chapter_seen_at = COALESCE(latest_release_at, created_at),
				latest_chapter_source_id = NULL,
				pending_lower_chapter = NULL,
				pending_lower_first_seen_at = NULL,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, target, item.TrackerID); err != nil {
			return repaired, fmt.Errorf("update tracker %d: %w", item.TrackerID, err)
		}
		repaired++
	}
	return repaired, nil
}

func storedLabel(value sql.NullFloat64) string {
	if !value.Valid {
		return "none"
	}
	return strconv.FormatFloat(value.Float64, 'f', -1, 64)
}
