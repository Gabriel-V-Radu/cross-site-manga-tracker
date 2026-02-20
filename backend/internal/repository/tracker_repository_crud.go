package repository

import (
	"database/sql"
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) SourceExists(sourceID int64) (bool, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(1) FROM sources WHERE id = ?`, sourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check source exists: %w", err)
	}
	return count > 0, nil
}

func (r *TrackerRepository) Create(tracker *models.Tracker) (*models.Tracker, error) {
	result, err := r.db.Exec(`
		INSERT INTO trackers (
			profile_id, title, source_id, source_item_id, source_url, status, last_read_chapter, latest_known_chapter, latest_release_at, last_checked_at, last_read_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)
	`, tracker.ProfileID, tracker.Title, tracker.SourceID, tracker.SourceItemID, tracker.SourceURL, tracker.Status, tracker.LastReadChapter, tracker.LatestKnownChapter, tracker.LatestReleaseAt, tracker.LastCheckedAt, tracker.LastReadChapter)
	if err != nil {
		return nil, fmt.Errorf("insert tracker: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get tracker last insert id: %w", err)
	}

	if err := r.ReplaceTrackerSources(tracker.ProfileID, id, []models.TrackerSource{{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}}); err != nil {
		return nil, fmt.Errorf("create tracker sources: %w", err)
	}

	return r.GetByID(tracker.ProfileID, id)
}

func (r *TrackerRepository) GetByID(profileID int64, id int64) (*models.Tracker, error) {
	row := r.db.QueryRow(`
		SELECT
			id, profile_id, title, source_id, source_item_id, source_url, status,
			last_read_chapter, last_read_at, latest_known_chapter, latest_release_at, last_checked_at,
			created_at, updated_at
		FROM trackers
		WHERE id = ? AND profile_id = ?
	`, id, profileID)

	tracker, err := scanTracker(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get tracker by id: %w", err)
	}

	tagsByTracker, err := r.ListTagsByTrackerIDs(profileID, []int64{tracker.ID})
	if err != nil {
		return nil, fmt.Errorf("get tracker tags: %w", err)
	}
	tracker.Tags = tagsByTracker[tracker.ID]

	return tracker, nil
}

func (r *TrackerRepository) Update(profileID int64, id int64, tracker *models.Tracker) (*models.Tracker, error) {
	result, err := r.db.Exec(`
		UPDATE trackers
		SET
			title = ?,
			source_id = ?,
			source_item_id = ?,
			source_url = ?,
			status = ?,
			last_read_chapter = ?,
			last_read_at = CASE WHEN last_read_chapter IS NOT ? THEN CURRENT_TIMESTAMP ELSE last_read_at END,
			latest_known_chapter = ?,
			latest_release_at = ?,
			last_checked_at = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND profile_id = ?
		  AND (
			title IS NOT ?
			OR source_id IS NOT ?
			OR source_item_id IS NOT ?
			OR source_url IS NOT ?
			OR status IS NOT ?
			OR last_read_chapter IS NOT ?
			OR latest_known_chapter IS NOT ?
			OR latest_release_at IS NOT ?
			OR last_checked_at IS NOT ?
		  )
	`,
		tracker.Title,
		tracker.SourceID,
		tracker.SourceItemID,
		tracker.SourceURL,
		tracker.Status,
		tracker.LastReadChapter,
		tracker.LastReadChapter,
		tracker.LatestKnownChapter,
		tracker.LatestReleaseAt,
		tracker.LastCheckedAt,
		id,
		profileID,
		tracker.Title,
		tracker.SourceID,
		tracker.SourceItemID,
		tracker.SourceURL,
		tracker.Status,
		tracker.LastReadChapter,
		tracker.LatestKnownChapter,
		tracker.LatestReleaseAt,
		tracker.LastCheckedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update tracker: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("tracker update rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return r.GetByID(profileID, id)
	}

	if err := r.UpsertTrackerSource(profileID, id, models.TrackerSource{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}); err != nil {
		return nil, fmt.Errorf("upsert primary tracker source: %w", err)
	}

	return r.GetByID(profileID, id)
}

func (r *TrackerRepository) UpdateLastReadChapter(profileID int64, id int64, lastReadChapter *float64) (bool, error) {
	result, err := r.db.Exec(`
		UPDATE trackers
		SET
			last_read_chapter = ?,
			last_read_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND profile_id = ?
		  AND last_read_chapter IS NOT ?
	`, lastReadChapter, id, profileID, lastReadChapter)
	if err != nil {
		return false, fmt.Errorf("update last read chapter: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("last read update rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

func (r *TrackerRepository) Delete(profileID int64, id int64) (bool, error) {
	result, err := r.db.Exec(`DELETE FROM trackers WHERE id = ? AND profile_id = ?`, id, profileID)
	if err != nil {
		return false, fmt.Errorf("delete tracker: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("tracker delete rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}
