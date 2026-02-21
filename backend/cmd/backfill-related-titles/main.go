package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

type trackerRecord struct {
	ID            int64
	ProfileID     int64
	Title         string
	SourceURL     string
	SourceKey     string
	RelatedTitles []string
}

type summary struct {
	Total      int
	Updated    int
	Unchanged  int
	Skipped    int
	Failed     int
	SkippedErr int
}

func main() {
	var (
		profileID      = flag.Int64("profile-id", 0, "Only process a single profile id (0 = all)")
		limit          = flag.Int("limit", 0, "Limit number of trackers processed (0 = all)")
		resolveTimeout = flag.Duration("resolve-timeout", 12*time.Second, "Per-tracker resolve timeout")
		dryRun         = flag.Bool("dry-run", false, "Preview updates without writing to DB")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler)
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

	registry, registryErr := connectordefaults.NewRegistry(cfg.YAMLConnectorsPath)
	if registryErr != nil {
		slog.Warn("connector registry loaded with warnings", "error", registryErr)
	}

	items, err := listTrackersForBackfill(db, *profileID, *limit)
	if err != nil {
		slog.Error("failed to list trackers", "error", err)
		os.Exit(1)
	}

	if len(items) == 0 {
		slog.Info("no trackers found for backfill", "profile_id", *profileID, "limit", *limit)
		return
	}

	stats := summary{}
	for _, item := range items {
		stats.Total++
		trimmedURL := strings.TrimSpace(item.SourceURL)
		if trimmedURL == "" {
			stats.Skipped++
			continue
		}

		connector, ok := registry.Get(item.SourceKey)
		if !ok {
			stats.SkippedErr++
			slog.Warn("connector not found; skipping tracker", "tracker_id", item.ID, "source_key", item.SourceKey)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), *resolveTimeout)
		resolved, resolveErr := connector.ResolveByURL(ctx, trimmedURL)
		cancel()
		if resolveErr != nil || resolved == nil {
			stats.Failed++
			if resolveErr != nil {
				slog.Warn("resolve failed; skipping tracker", "tracker_id", item.ID, "source_key", item.SourceKey, "error", resolveErr)
			} else {
				slog.Warn("resolve returned empty result; skipping tracker", "tracker_id", item.ID, "source_key", item.SourceKey)
			}
			continue
		}

		newRelatedTitles := buildStoredRelatedTitles(item.Title, resolved.Title, resolved.RelatedTitles)
		if relatedTitleListsEqual(item.RelatedTitles, newRelatedTitles) {
			stats.Unchanged++
			continue
		}

		if *dryRun {
			stats.Updated++
			slog.Info("would update related titles", "tracker_id", item.ID, "before", item.RelatedTitles, "after", newRelatedTitles)
			continue
		}

		if err := updateTrackerRelatedTitles(db, item.ID, newRelatedTitles); err != nil {
			stats.Failed++
			slog.Warn("failed to update related titles", "tracker_id", item.ID, "error", err)
			continue
		}

		stats.Updated++
	}

	slog.Info(
		"backfill completed",
		"dry_run", *dryRun,
		"total", stats.Total,
		"updated", stats.Updated,
		"unchanged", stats.Unchanged,
		"skipped", stats.Skipped,
		"skipped_missing_connector", stats.SkippedErr,
		"failed", stats.Failed,
	)
}

func listTrackersForBackfill(db *sql.DB, profileID int64, limit int) ([]trackerRecord, error) {
	queryBuilder := strings.Builder{}
	queryBuilder.WriteString(`
		SELECT
			t.id,
			t.profile_id,
			t.title,
			t.source_url,
			s.key,
			COALESCE(t.related_titles, '')
		FROM trackers t
		INNER JOIN sources s ON s.id = t.source_id
		WHERE s.enabled = 1
	`)

	args := make([]any, 0, 2)
	if profileID > 0 {
		queryBuilder.WriteString(` AND t.profile_id = ?`)
		args = append(args, profileID)
	}

	queryBuilder.WriteString(` ORDER BY t.id ASC`)
	if limit > 0 {
		queryBuilder.WriteString(` LIMIT ?`)
		args = append(args, limit)
	}

	rows, err := db.Query(queryBuilder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query trackers: %w", err)
	}
	defer rows.Close()

	trackers := make([]trackerRecord, 0)
	for rows.Next() {
		var (
			item             trackerRecord
			relatedTitlesRaw string
		)
		if err := rows.Scan(
			&item.ID,
			&item.ProfileID,
			&item.Title,
			&item.SourceURL,
			&item.SourceKey,
			&relatedTitlesRaw,
		); err != nil {
			return nil, fmt.Errorf("scan tracker row: %w", err)
		}
		item.RelatedTitles = decodeStoredRelatedTitles(relatedTitlesRaw)
		trackers = append(trackers, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker rows: %w", err)
	}

	return trackers, nil
}

func updateTrackerRelatedTitles(db *sql.DB, trackerID int64, relatedTitles []string) error {
	encoded := encodeStoredRelatedTitles(relatedTitles)
	var relatedTitlesValue any
	if encoded != "" {
		relatedTitlesValue = encoded
	}

	_, err := db.Exec(`
		UPDATE trackers
		SET related_titles = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, relatedTitlesValue, trackerID)
	if err != nil {
		return fmt.Errorf("update tracker %d: %w", trackerID, err)
	}

	return nil
}

func buildStoredRelatedTitles(trackerTitle string, resolvedTitle string, resolvedRelatedTitles []string) []string {
	filtered := searchutil.FilterEnglishAlphabetNames(resolvedRelatedTitles)
	if len(filtered) == 0 {
		return nil
	}

	normalizedMainTitles := map[string]struct{}{}
	if normalized := searchutil.Normalize(resolvedTitle); normalized != "" {
		normalizedMainTitles[normalized] = struct{}{}
	}
	if normalized := searchutil.Normalize(trackerTitle); normalized != "" {
		normalizedMainTitles[normalized] = struct{}{}
	}

	relatedOnly := make([]string, 0, len(filtered))
	for _, candidate := range filtered {
		normalizedCandidate := searchutil.Normalize(candidate)
		if normalizedCandidate == "" {
			continue
		}
		if _, isMainTitle := normalizedMainTitles[normalizedCandidate]; isMainTitle {
			continue
		}
		relatedOnly = append(relatedOnly, candidate)
	}

	if len(relatedOnly) == 0 {
		return nil
	}

	return relatedOnly
}

func decodeStoredRelatedTitles(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil
	}

	return searchutil.FilterEnglishAlphabetNames(values)
}

func encodeStoredRelatedTitles(values []string) string {
	sanitized := searchutil.FilterEnglishAlphabetNames(values)
	if len(sanitized) == 0 {
		return ""
	}

	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return ""
	}

	return string(encoded)
}

func relatedTitleListsEqual(a []string, b []string) bool {
	normalize := func(values []string) []string {
		filtered := searchutil.FilterEnglishAlphabetNames(values)
		keys := make([]string, 0, len(filtered))
		for _, value := range filtered {
			key := searchutil.Normalize(value)
			if key == "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	}

	left := normalize(a)
	right := normalize(b)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
