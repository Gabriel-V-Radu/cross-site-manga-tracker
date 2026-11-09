package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

// CountTrackersBySource reports how many of a profile's trackers each source
// serves, counting a tracker once per source whether it is the primary or a
// linked alternate. The dashboard uses it to rank the connector shortcuts.
func (r *TrackerRepository) CountTrackersBySource(ctx context.Context, profileID int64) (map[int64]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT source_id, COUNT(1)
		FROM (
			SELECT id AS tracker_id, source_id FROM trackers WHERE profile_id = ?
			UNION
			SELECT ts.tracker_id, ts.source_id
			FROM tracker_sources ts
			INNER JOIN trackers t ON t.id = ts.tracker_id
			WHERE t.profile_id = ?
		)
		WHERE source_id > 0
		GROUP BY source_id
	`, profileID, profileID)
	if err != nil {
		return nil, fmt.Errorf("count trackers by source: %w", err)
	}
	defer rows.Close()

	counts := map[int64]int{}
	for rows.Next() {
		var sourceID int64
		var count int
		if err := rows.Scan(&sourceID, &count); err != nil {
			return nil, fmt.Errorf("scan tracker count by source: %w", err)
		}
		counts[sourceID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker counts by source: %w", err)
	}
	return counts, nil
}

func (r *TrackerRepository) ListTrackerSources(ctx context.Context, profileID int64, trackerID int64) ([]models.TrackerSource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			ts.id,
			ts.tracker_id,
			ts.source_id,
			s.name,
			ts.source_item_id,
			ts.source_url,
			ts.created_at,
			ts.updated_at
		FROM tracker_sources ts
		INNER JOIN trackers t ON t.id = ts.tracker_id
		INNER JOIN sources s ON s.id = ts.source_id
		WHERE ts.tracker_id = ?
		  AND t.profile_id = ?
		ORDER BY s.name ASC, ts.id ASC
	`, trackerID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list tracker sources: %w", err)
	}
	defer rows.Close()

	items := make([]models.TrackerSource, 0)
	for rows.Next() {
		var item models.TrackerSource
		var sourceItemID sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.TrackerID,
			&item.SourceID,
			&item.SourceName,
			&sourceItemID,
			&item.SourceURL,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan tracker source: %w", err)
		}
		if sourceItemID.Valid {
			item.SourceItemID = &sourceItemID.String
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker sources: %w", err)
	}

	return items, nil
}

func (r *TrackerRepository) ReplaceTrackerSources(ctx context.Context, profileID int64, trackerID int64, sources []models.TrackerSource) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace tracker sources tx: %w", err)
	}
	defer tx.Rollback()

	if err := replaceTrackerSourcesInTx(ctx, tx, profileID, trackerID, sources); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace tracker sources tx: %w", err)
	}

	return nil
}

// replaceTrackerSourcesInTx is ReplaceTrackerSources' body on a caller-owned
// transaction, so Create can run the tracker INSERT and its source rows as one
// atomic write. The caller commits or rolls back.
//
// The list replaces the tracker's links, but a link that is in both the old
// set and the new one keeps its row. This used to be DELETE-all then INSERT,
// and the edit form sends the whole list on every save: a title or tag edit
// recreated every row, which reset the poll rotation's memory (last_polled_at,
// in_step — see scheduler.planPoll) for every site the tracker is linked to.
// The next cycles then asked the sites in their starting order again and
// stopped trusting every alternate until it had answered in step once more.
//
// A link is the same link when its source and LOWER(TRIM(source_url)) match,
// the key the poller records its polls under. A tracker the profile does not
// own is left alone.
func replaceTrackerSourcesInTx(ctx context.Context, tx *sql.Tx, profileID int64, trackerID int64, sources []models.TrackerSource) error {
	owned, err := trackerOwnedByProfileTx(ctx, tx, trackerID, profileID)
	if err != nil {
		return fmt.Errorf("check tracker ownership for sources: %w", err)
	}
	if !owned {
		return nil
	}

	// Two entries of the list can name the same (source, url) — the edit form
	// canonicalizes each link to the site's own URL, so a reader link and the
	// series page become one — and the second used to trip the unique key and
	// roll back the whole save. The duplicate finds the row the first one
	// wrote and folds into it, keeping an item id either of them knew.
	keptIDs := make([]any, 0, len(sources))
	for _, source := range sources {
		sourceURL := strings.TrimSpace(source.SourceURL)
		if sourceURL == "" || source.SourceID <= 0 {
			continue
		}
		rowID, err := keepOrInsertTrackerSourceTx(ctx, tx, trackerID, source.SourceID, source.SourceItemID, sourceURL)
		if err != nil {
			return err
		}
		keptIDs = append(keptIDs, rowID)
	}

	// Whatever the list did not name goes, including a second stored spelling
	// of a link it did name: one row per link is what a replace leaves behind.
	query := `DELETE FROM tracker_sources WHERE tracker_id = ?`
	args := []any{trackerID}
	if len(keptIDs) > 0 {
		query += ` AND id NOT IN (` + sqlPlaceholders(len(keptIDs)) + `)`
		args = append(args, keptIDs...)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("delete tracker sources: %w", err)
	}

	return nil
}

// keepOrInsertTrackerSourceTx makes one entry of a replaced list a row and
// returns the row's id: the stored row for that link when there is one, a new
// row otherwise. A stored row takes the submitted spelling of the URL and an
// item id the entry knows (an entry without one keeps the stored id), and
// nothing else of it is touched — its created_at and the rotation's memory
// stay as they were. updated_at moves only when the row actually changed.
func keepOrInsertTrackerSourceTx(ctx context.Context, tx *sql.Tx, trackerID int64, sourceID int64, sourceItemID *string, sourceURL string) (int64, error) {
	// Rows that differ only in case or padding can both be stored. The one
	// spelled exactly as submitted is preferred, so rewriting the URL below
	// can never collide with it on the table's unique key.
	var rowID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM tracker_sources
		WHERE tracker_id = ? AND source_id = ? AND LOWER(TRIM(source_url)) = LOWER(TRIM(?))
		ORDER BY (source_url = ?) DESC, id ASC
		LIMIT 1
	`, trackerID, sourceID, sourceURL, sourceURL).Scan(&rowID)
	if err == sql.ErrNoRows {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
			VALUES (?, ?, ?, ?)
		`, trackerID, sourceID, sourceItemID, sourceURL)
		if err != nil {
			return 0, fmt.Errorf("insert tracker source: %w", err)
		}
		rowID, err = result.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("get tracker source insert id: %w", err)
		}
		return rowID, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find tracker source: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE tracker_sources
		SET source_url = ?,
			source_item_id = COALESCE(?, source_item_id),
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND (source_url IS NOT ? OR source_item_id IS NOT COALESCE(?, source_item_id))
	`, sourceURL, sourceItemID, rowID, sourceURL, sourceItemID); err != nil {
		return 0, fmt.Errorf("update tracker source: %w", err)
	}
	return rowID, nil
}

func (r *TrackerRepository) UpsertTrackerSource(ctx context.Context, profileID int64, trackerID int64, source models.TrackerSource) error {
	if source.SourceID <= 0 || strings.TrimSpace(source.SourceURL) == "" {
		return nil
	}

	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM trackers WHERE id = ? AND profile_id = ?`, trackerID, profileID).Scan(&exists); err != nil {
		return fmt.Errorf("check tracker ownership: %w", err)
	}
	if exists == 0 {
		return nil
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tracker_id, source_id, source_url)
		DO UPDATE SET
			source_item_id = excluded.source_item_id,
			updated_at = CURRENT_TIMESTAMP
	`, trackerID, source.SourceID, source.SourceItemID, strings.TrimSpace(source.SourceURL))
	if err != nil {
		return fmt.Errorf("upsert tracker source: %w", err)
	}

	return nil
}
