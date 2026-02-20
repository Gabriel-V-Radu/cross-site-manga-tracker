package repository

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) ListLinkedSourceIDs(profileID int64) ([]int64, error) {
	rows, err := r.db.Query(`
		SELECT source_id
		FROM (
			SELECT source_id
			FROM trackers
			WHERE profile_id = ?
			UNION
			SELECT ts.source_id
			FROM tracker_sources ts
			INNER JOIN trackers t ON t.id = ts.tracker_id
			WHERE t.profile_id = ?
		)
		WHERE source_id > 0
		ORDER BY source_id ASC
	`, profileID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list linked source ids: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var sourceID int64
		if err := rows.Scan(&sourceID); err != nil {
			return nil, fmt.Errorf("scan linked source id: %w", err)
		}
		ids = append(ids, sourceID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate linked source ids: %w", err)
	}

	return ids, nil
}

func (r *TrackerRepository) ListTrackerSources(profileID int64, trackerID int64) ([]models.TrackerSource, error) {
	rows, err := r.db.Query(`
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

func (r *TrackerRepository) ReplaceTrackerSources(profileID int64, trackerID int64, sources []models.TrackerSource) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replace tracker sources tx: %w", err)
	}

	if _, err := tx.Exec(`
		DELETE FROM tracker_sources
		WHERE tracker_id = ?
		  AND EXISTS (SELECT 1 FROM trackers t WHERE t.id = ? AND t.profile_id = ?)
	`, trackerID, trackerID, profileID); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete tracker sources: %w", err)
	}

	for _, source := range sources {
		if strings.TrimSpace(source.SourceURL) == "" || source.SourceID <= 0 {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
			VALUES (?, ?, ?, ?)
		`, trackerID, source.SourceID, source.SourceItemID, strings.TrimSpace(source.SourceURL)); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert tracker source: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace tracker sources tx: %w", err)
	}

	return nil
}

func (r *TrackerRepository) UpsertTrackerSource(profileID int64, trackerID int64, source models.TrackerSource) error {
	if source.SourceID <= 0 || strings.TrimSpace(source.SourceURL) == "" {
		return nil
	}

	var exists int
	if err := r.db.QueryRow(`SELECT COUNT(1) FROM trackers WHERE id = ? AND profile_id = ?`, trackerID, profileID).Scan(&exists); err != nil {
		return fmt.Errorf("check tracker ownership: %w", err)
	}
	if exists == 0 {
		return nil
	}

	_, err := r.db.Exec(`
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
