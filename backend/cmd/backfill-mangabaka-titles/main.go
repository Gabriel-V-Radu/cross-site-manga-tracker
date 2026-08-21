// Command backfill-mangabaka-titles walks the whole library once and merges
// each tracker's alternate titles from MangaBaka into related_titles. The
// link scan already does this per-tracker when a scan visits it
// (linkscan.lookupAid → MergeRelatedTitles); this command is the same
// conservative lookup applied to every tracker, so future scans match exactly
// on the first try and the dashboard search finds series under any name.
//
// A MangaBaka record is only accepted when one of its titles matches the
// tracker's title or stored related titles exactly (after
// searchutil.Normalize) — a wrong record would poison matching, so near
// matches are not good enough. Merging is idempotent: re-running the command
// never duplicates titles, and -start-id resumes an interrupted pass.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

const (
	perRequestTimeout = 30 * time.Second
	// maxQueriesPerTracker bounds the MangaBaka searches spent on one tracker:
	// the primary title, then at most two stored alternates when the primary
	// query finds no exact match.
	maxQueriesPerTracker = 3
	// maxCooldownRetries bounds how often one tracker waits out the host's
	// circuit breaker before the pass gives up and prints how to resume.
	maxCooldownRetries = 5
)

type searcher interface {
	Search(ctx context.Context, query string, limit int) ([]mangabaka.Series, error)
}

type trackerRow struct {
	ID            int64
	Title         string
	RelatedTitles []string
}

type stats struct {
	Total       int
	Matched     int
	TitlesAdded int
	NoRecord    int
	Unchanged   int
	Errors      int
}

func main() {
	var (
		profileID = flag.Int64("profile-id", 0, "Only process a single profile id (0 = all)")
		startID   = flag.Int64("start-id", 0, "Resume from this tracker id (inclusive)")
		limit     = flag.Int("limit", 0, "Limit number of trackers processed (0 = all)")
		dryRun    = flag.Bool("dry-run", false, "Preview merges without writing to DB")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	db, err := database.Open(cfg.SQLitePath)
	if err != nil {
		slog.Error("failed to open sqlite", "path", cfg.SQLitePath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := database.ApplyMigrations(db, cfg.MigrationsPath); err != nil {
		slog.Error("failed to apply migrations", "error", err)
		os.Exit(1)
	}

	result, err := runBackfill(db, mangabaka.NewClient(), *profileID, *startID, *limit, *dryRun)
	if err != nil {
		slog.Error("backfill aborted", "error", err)
		os.Exit(1)
	}

	slog.Info(
		"mangabaka backfill completed",
		"dry_run", *dryRun,
		"total", result.Total,
		"matched", result.Matched,
		"titles_added", result.TitlesAdded,
		"already_complete", result.Unchanged,
		"no_record", result.NoRecord,
		"errors", result.Errors,
	)
}

func runBackfill(db *sql.DB, aid searcher, profileID int64, startID int64, limit int, dryRun bool) (stats, error) {
	trackers, err := listTrackers(db, profileID, startID, limit)
	if err != nil {
		return stats{}, fmt.Errorf("list trackers: %w", err)
	}

	suggestions := repository.NewLinkSuggestionRepository(db)
	// Two profiles often track the same series; one search serves both.
	searchCache := map[string][]mangabaka.Series{}

	result := stats{}
	for _, tracker := range trackers {
		result.Total++

		series, err := findExactRecord(aid, searchCache, tracker)
		if err != nil {
			var cooling *connectors.SourceCoolingDownError
			if errors.As(err, &cooling) {
				return result, fmt.Errorf(
					"mangabaka kept cooling down at tracker %d; resume with -start-id %d: %w",
					tracker.ID, tracker.ID, err,
				)
			}
			result.Errors++
			slog.Warn("mangabaka lookup failed; skipping tracker", "tracker_id", tracker.ID, "title", tracker.Title, "error", err)
			continue
		}
		if series == nil {
			result.NoRecord++
			continue
		}

		newTitles := titlesNotStored(tracker, series.Titles)
		if len(newTitles) == 0 {
			result.Unchanged++
			continue
		}

		result.Matched++
		result.TitlesAdded += len(newTitles)
		if dryRun {
			slog.Info("would merge related titles", "tracker_id", tracker.ID, "title", tracker.Title, "new_titles", newTitles)
			continue
		}

		if err := suggestions.MergeRelatedTitles(tracker.ID, series.Titles); err != nil {
			result.Matched--
			result.TitlesAdded -= len(newTitles)
			result.Errors++
			slog.Warn("merge related titles failed", "tracker_id", tracker.ID, "error", err)
			continue
		}
		slog.Info("merged related titles", "tracker_id", tracker.ID, "title", tracker.Title, "new_titles", newTitles)
	}

	return result, nil
}

// findExactRecord searches MangaBaka by the tracker's title, falling back to a
// couple of stored alternates, and accepts a record only on an exact
// normalized title match against the tracker's known names — the same rule as
// linkscan.lookupAid. Waits out the shared throttle's cooldown a few times
// before giving up.
func findExactRecord(aid searcher, cache map[string][]mangabaka.Series, tracker trackerRow) (*mangabaka.Series, error) {
	trackerTitles := map[string]struct{}{}
	for _, title := range append([]string{tracker.Title}, tracker.RelatedTitles...) {
		if normalized := searchutil.Normalize(title); normalized != "" {
			trackerTitles[normalized] = struct{}{}
		}
	}

	queries := []string{tracker.Title}
	for _, alternate := range searchutil.FilterEnglishAlphabetNames(tracker.RelatedTitles) {
		if len(queries) >= maxQueriesPerTracker {
			break
		}
		if searchutil.Normalize(alternate) == searchutil.Normalize(tracker.Title) {
			continue
		}
		queries = append(queries, alternate)
	}

	for _, query := range queries {
		results, err := searchWithCooldownRetry(aid, cache, query)
		if err != nil {
			return nil, err
		}
		for index, series := range results {
			for _, title := range series.Titles {
				if _, ok := trackerTitles[searchutil.Normalize(title)]; ok {
					return &results[index], nil
				}
			}
		}
	}
	return nil, nil
}

func searchWithCooldownRetry(aid searcher, cache map[string][]mangabaka.Series, query string) ([]mangabaka.Series, error) {
	cacheKey := searchutil.Normalize(query)
	if cached, ok := cache[cacheKey]; ok {
		return cached, nil
	}

	var lastErr error
	for attempt := 0; attempt <= maxCooldownRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), perRequestTimeout)
		results, err := aid.Search(ctx, query, 8)
		cancel()
		if err == nil {
			cache[cacheKey] = results
			return results, nil
		}
		lastErr = err

		var cooling *connectors.SourceCoolingDownError
		if !errors.As(err, &cooling) {
			return nil, err
		}
		wait := cooling.RetryAfter + time.Second
		if wait > 15*time.Minute {
			wait = 15 * time.Minute
		}
		slog.Info("mangabaka is cooling down; waiting", "query", query, "wait", wait.Round(time.Second))
		time.Sleep(wait)
	}
	return nil, lastErr
}

// titlesNotStored reports which of the record's titles would actually be new
// on this tracker, applying the same latin-alphabet filter the merge does.
// The tracker's own title is not counted as new even though MergeRelatedTitles
// would store it — an "added" count should mean names the search could not
// match before.
func titlesNotStored(tracker trackerRow, recordTitles []string) []string {
	stored := map[string]struct{}{}
	for _, title := range append([]string{tracker.Title}, tracker.RelatedTitles...) {
		if normalized := searchutil.Normalize(title); normalized != "" {
			stored[normalized] = struct{}{}
		}
	}

	fresh := make([]string, 0)
	for _, candidate := range searchutil.FilterEnglishAlphabetNames(recordTitles) {
		normalized := searchutil.Normalize(candidate)
		if normalized == "" {
			continue
		}
		if _, exists := stored[normalized]; exists {
			continue
		}
		stored[normalized] = struct{}{}
		fresh = append(fresh, candidate)
	}
	return fresh
}

func listTrackers(db *sql.DB, profileID int64, startID int64, limit int) ([]trackerRow, error) {
	query := strings.Builder{}
	query.WriteString(`
		SELECT t.id, t.title, COALESCE(t.related_titles, '')
		FROM trackers t
		WHERE t.id >= ?
	`)
	args := []any{startID}
	if profileID > 0 {
		query.WriteString(` AND t.profile_id = ?`)
		args = append(args, profileID)
	}
	query.WriteString(` ORDER BY t.id ASC`)
	if limit > 0 {
		query.WriteString(` LIMIT ?`)
		args = append(args, limit)
	}

	rows, err := db.Query(query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	trackers := make([]trackerRow, 0)
	for rows.Next() {
		var (
			tracker trackerRow
			raw     string
		)
		if err := rows.Scan(&tracker.ID, &tracker.Title, &raw); err != nil {
			return nil, err
		}
		tracker.RelatedTitles = decodeRelatedTitles(raw)
		trackers = append(trackers, tracker)
	}
	return trackers, rows.Err()
}

func decodeRelatedTitles(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil
	}
	return values
}
