package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
)

type sourceUsage struct {
	ID              int64
	Key             string
	Name            string
	ConnectorKind   string
	Enabled         bool
	PrimaryTrackers int64
	LinkedSources   int64
	ProfileLogos    int64
}

type linkedSourceCandidate struct {
	SourceID     int64
	SourceKey    string
	SourceURL    string
	SourceItemID *string
}

type trackerPromotion struct {
	TrackerID       int64
	OldSourceID     int64
	OldSourceKey    string
	NewSourceID     int64
	NewSourceKey    string
	NewSourceURL    string
	NewSourceItemID *string
}

type stalePrimaryTracker struct {
	TrackerID int64
	SourceID  int64
	SourceKey string
}

type cleanupOutcome struct {
	PromotedTrackers   int64
	DeletedTrackers    int64
	DeletedLinks       int64
	DeletedSourceLogos int64
	DeletedSources     int64
}

func main() {
	var apply bool
	flag.BoolVar(&apply, "apply", false, "Apply cleanup changes. Without this flag, the command is a dry-run preview.")
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

	activeSourceKeys := buildActiveSourceKeySet()
	slog.Info("loaded active source keys from registry", "count", len(activeSourceKeys), "keys", sortedMapKeys(activeSourceKeys))

	staleSources, staleSourceKeyByID, err := listStaleSources(db, activeSourceKeys)
	if err != nil {
		slog.Error("failed to list stale sources", "error", err)
		os.Exit(1)
	}

	if len(staleSources) == 0 {
		slog.Info("no stale sources found; nothing to clean")
		return
	}

	for _, source := range staleSources {
		slog.Info(
			"stale source detected",
			"source_id", source.ID,
			"key", source.Key,
			"name", source.Name,
			"connector_kind", source.ConnectorKind,
			"enabled", source.Enabled,
			"primary_trackers", source.PrimaryTrackers,
			"linked_sources", source.LinkedSources,
			"profile_logos", source.ProfileLogos,
		)
	}

	staleSourceIDs := make(map[int64]struct{}, len(staleSources))
	for _, source := range staleSources {
		staleSourceIDs[source.ID] = struct{}{}
	}

	promotions, orphanedTrackers, err := planTrackerPrimarySourcePromotions(db, staleSourceIDs, staleSourceKeyByID)
	if err != nil {
		slog.Error("failed to plan tracker promotions", "error", err)
		os.Exit(1)
	}

	for _, promotion := range promotions {
		slog.Info(
			"tracker primary source will be promoted",
			"tracker_id", promotion.TrackerID,
			"old_source_id", promotion.OldSourceID,
			"old_source_key", promotion.OldSourceKey,
			"new_source_id", promotion.NewSourceID,
			"new_source_key", promotion.NewSourceKey,
		)
	}

	for _, tracker := range orphanedTrackers {
		slog.Warn(
			"tracker has no active linked source; it will be deleted with stale source cleanup",
			"tracker_id", tracker.TrackerID,
			"stale_source_id", tracker.SourceID,
			"stale_source_key", tracker.SourceKey,
		)
	}

	if !apply {
		slog.Info(
			"dry-run complete",
			"stale_sources", len(staleSources),
			"trackers_to_promote", len(promotions),
			"trackers_to_delete", len(orphanedTrackers),
			"linked_rows_to_delete", sumLinkedRowCounts(staleSources),
		)
		return
	}

	outcome, err := applyCleanup(db, staleSources, promotions)
	if err != nil {
		slog.Error("failed to apply stale source cleanup", "error", err)
		os.Exit(1)
	}

	slog.Info(
		"cleanup completed",
		"stale_sources", len(staleSources),
		"promoted_trackers", outcome.PromotedTrackers,
		"deleted_trackers", outcome.DeletedTrackers,
		"deleted_tracker_sources", outcome.DeletedLinks,
		"deleted_profile_source_logos", outcome.DeletedSourceLogos,
		"deleted_sources", outcome.DeletedSources,
	)
}

func buildActiveSourceKeySet() map[string]struct{} {
	registry := connectordefaults.NewRegistry()
	descriptors := registry.List()

	keys := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		key := normalizeSourceKey(descriptor.Key)
		if key == "" {
			continue
		}
		keys[key] = struct{}{}
	}

	return keys
}

func listStaleSources(db *sql.DB, activeSourceKeys map[string]struct{}) ([]sourceUsage, map[int64]string, error) {
	rows, err := db.Query(`
		SELECT
			s.id,
			s.key,
			s.name,
			s.connector_kind,
			s.enabled,
			(SELECT COUNT(1) FROM trackers t WHERE t.source_id = s.id) AS primary_trackers,
			(SELECT COUNT(1) FROM tracker_sources ts WHERE ts.source_id = s.id) AS linked_sources,
			(SELECT COUNT(1) FROM profile_source_logos psl WHERE psl.source_id = s.id) AS profile_logos
		FROM sources s
		ORDER BY s.key ASC
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("query sources: %w", err)
	}
	defer rows.Close()

	stale := make([]sourceUsage, 0)
	byID := make(map[int64]string)

	for rows.Next() {
		var item sourceUsage
		var enabled bool
		if err := rows.Scan(
			&item.ID,
			&item.Key,
			&item.Name,
			&item.ConnectorKind,
			&enabled,
			&item.PrimaryTrackers,
			&item.LinkedSources,
			&item.ProfileLogos,
		); err != nil {
			return nil, nil, fmt.Errorf("scan source row: %w", err)
		}
		item.Enabled = enabled

		if _, exists := activeSourceKeys[normalizeSourceKey(item.Key)]; exists {
			continue
		}

		stale = append(stale, item)
		byID[item.ID] = item.Key
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate source rows: %w", err)
	}

	return stale, byID, nil
}

func planTrackerPrimarySourcePromotions(db *sql.DB, staleSourceIDs map[int64]struct{}, staleSourceKeyByID map[int64]string) ([]trackerPromotion, []stalePrimaryTracker, error) {
	ids := sortedInt64MapKeys(staleSourceIDs)
	if len(ids) == 0 {
		return []trackerPromotion{}, []stalePrimaryTracker{}, nil
	}

	query := fmt.Sprintf(`
		SELECT id, source_id
		FROM trackers
		WHERE source_id IN (%s)
		ORDER BY id ASC
	`, placeholders(len(ids)))

	rows, err := db.Query(query, int64SliceToAny(ids)...)
	if err != nil {
		return nil, nil, fmt.Errorf("query trackers with stale primary source: %w", err)
	}
	defer rows.Close()

	promotions := make([]trackerPromotion, 0)
	orphaned := make([]stalePrimaryTracker, 0)

	for rows.Next() {
		var trackerID int64
		var staleSourceID int64
		if err := rows.Scan(&trackerID, &staleSourceID); err != nil {
			return nil, nil, fmt.Errorf("scan tracker stale primary row: %w", err)
		}

		candidate, err := firstActiveLinkedSource(db, trackerID, staleSourceIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("find linked source for tracker %d: %w", trackerID, err)
		}

		if candidate == nil {
			orphaned = append(orphaned, stalePrimaryTracker{
				TrackerID: trackerID,
				SourceID:  staleSourceID,
				SourceKey: staleSourceKeyByID[staleSourceID],
			})
			continue
		}

		promotions = append(promotions, trackerPromotion{
			TrackerID:       trackerID,
			OldSourceID:     staleSourceID,
			OldSourceKey:    staleSourceKeyByID[staleSourceID],
			NewSourceID:     candidate.SourceID,
			NewSourceKey:    candidate.SourceKey,
			NewSourceURL:    candidate.SourceURL,
			NewSourceItemID: candidate.SourceItemID,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate tracker stale primary rows: %w", err)
	}

	return promotions, orphaned, nil
}

func firstActiveLinkedSource(db *sql.DB, trackerID int64, staleSourceIDs map[int64]struct{}) (*linkedSourceCandidate, error) {
	rows, err := db.Query(`
		SELECT
			ts.source_id,
			s.key,
			ts.source_item_id,
			ts.source_url
		FROM tracker_sources ts
		INNER JOIN sources s ON s.id = ts.source_id
		WHERE ts.tracker_id = ?
		ORDER BY ts.id ASC
	`, trackerID)
	if err != nil {
		return nil, fmt.Errorf("query tracker linked sources: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			sourceID     int64
			sourceKey    string
			sourceItemID sql.NullString
			sourceURL    string
		)
		if err := rows.Scan(&sourceID, &sourceKey, &sourceItemID, &sourceURL); err != nil {
			return nil, fmt.Errorf("scan tracker linked source: %w", err)
		}

		if _, stale := staleSourceIDs[sourceID]; stale {
			continue
		}

		trimmedURL := strings.TrimSpace(sourceURL)
		if sourceID <= 0 || trimmedURL == "" {
			continue
		}

		var itemID *string
		if sourceItemID.Valid {
			trimmedItemID := strings.TrimSpace(sourceItemID.String)
			if trimmedItemID != "" {
				itemID = &trimmedItemID
			}
		}

		return &linkedSourceCandidate{
			SourceID:     sourceID,
			SourceKey:    sourceKey,
			SourceURL:    trimmedURL,
			SourceItemID: itemID,
		}, nil
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker linked sources: %w", err)
	}

	return nil, nil
}

func applyCleanup(db *sql.DB, staleSources []sourceUsage, promotions []trackerPromotion) (cleanupOutcome, error) {
	if len(staleSources) == 0 {
		return cleanupOutcome{}, nil
	}

	sourceIDs := make([]int64, 0, len(staleSources))
	for _, source := range staleSources {
		sourceIDs = append(sourceIDs, source.ID)
	}
	sort.Slice(sourceIDs, func(i, j int) bool {
		return sourceIDs[i] < sourceIDs[j]
	})

	tx, err := db.Begin()
	if err != nil {
		return cleanupOutcome{}, fmt.Errorf("begin cleanup tx: %w", err)
	}

	rollback := func() {
		_ = tx.Rollback()
	}

	var outcome cleanupOutcome

	for _, promotion := range promotions {
		var sourceItemID any
		if promotion.NewSourceItemID != nil {
			trimmed := strings.TrimSpace(*promotion.NewSourceItemID)
			if trimmed != "" {
				sourceItemID = trimmed
			}
		}

		result, err := tx.Exec(`
			UPDATE trackers
			SET
				source_id = ?,
				source_item_id = ?,
				source_url = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
			  AND source_id = ?
		`, promotion.NewSourceID, sourceItemID, strings.TrimSpace(promotion.NewSourceURL), promotion.TrackerID, promotion.OldSourceID)
		if err != nil {
			rollback()
			return cleanupOutcome{}, fmt.Errorf("promote tracker %d source: %w", promotion.TrackerID, err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			rollback()
			return cleanupOutcome{}, fmt.Errorf("promotion rows affected tracker %d: %w", promotion.TrackerID, err)
		}
		outcome.PromotedTrackers += rowsAffected
	}

	outcome.DeletedLinks, err = deleteBySourceID(tx, "tracker_sources", "source_id", sourceIDs)
	if err != nil {
		rollback()
		return cleanupOutcome{}, err
	}

	outcome.DeletedTrackers, err = deleteBySourceID(tx, "trackers", "source_id", sourceIDs)
	if err != nil {
		rollback()
		return cleanupOutcome{}, err
	}

	outcome.DeletedSourceLogos, err = deleteBySourceID(tx, "profile_source_logos", "source_id", sourceIDs)
	if err != nil {
		rollback()
		return cleanupOutcome{}, err
	}

	outcome.DeletedSources, err = deleteBySourceID(tx, "sources", "id", sourceIDs)
	if err != nil {
		rollback()
		return cleanupOutcome{}, err
	}

	if err := tx.Commit(); err != nil {
		return cleanupOutcome{}, fmt.Errorf("commit cleanup tx: %w", err)
	}

	return outcome, nil
}

func deleteBySourceID(tx *sql.Tx, table string, column string, sourceIDs []int64) (int64, error) {
	if len(sourceIDs) == 0 {
		return 0, nil
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)", table, column, placeholders(len(sourceIDs)))
	result, err := tx.Exec(query, int64SliceToAny(sourceIDs)...)
	if err != nil {
		return 0, fmt.Errorf("delete from %s: %w", table, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected for %s delete: %w", table, err)
	}

	return rowsAffected, nil
}

func normalizeSourceKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedInt64MapKeys(values map[int64]struct{}) []int64 {
	keys := make([]int64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i] < keys[j]
	})
	return keys
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}

	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func int64SliceToAny(values []int64) []any {
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	return items
}

func sumLinkedRowCounts(staleSources []sourceUsage) int64 {
	var total int64
	for _, source := range staleSources {
		total += source.LinkedSources
	}
	return total
}
