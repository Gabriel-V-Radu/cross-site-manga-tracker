package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) SourceExists(sourceID int64) (bool, error) {
	return r.SourceExistsContext(context.Background(), sourceID)
}

func (r *TrackerRepository) SourceExistsContext(ctx context.Context, sourceID int64) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sources WHERE id = ?`, sourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check source exists: %w", err)
	}
	return count > 0, nil
}

func (r *TrackerRepository) Create(tracker *models.Tracker) (*models.Tracker, error) {
	return r.CreateContext(context.Background(), tracker)
}

func (r *TrackerRepository) CreateContext(ctx context.Context, tracker *models.Tracker) (*models.Tracker, error) {
	relatedTitlesJSON := encodeRelatedTitlesJSON(tracker.RelatedTitles)

	// The tracker row and its primary source row are one write: committing the
	// INSERT and then failing to link the source used to leave a tracker with
	// no tracker_sources rows behind. With MaxOpenConns(1) everything up to
	// Commit must run on the tx handle — including GetByID staying AFTER the
	// commit, since it would otherwise wait on the connection this tx holds.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create tracker tx: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO trackers (
			profile_id, title, related_titles, source_id, source_item_id, source_url, status, last_read_chapter, rating, latest_known_chapter, latest_release_at, latest_chapter_seen_at, last_checked_at, last_read_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END, ?, CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)
	`, tracker.ProfileID, tracker.Title, relatedTitlesJSON, tracker.SourceID, tracker.SourceItemID, tracker.SourceURL, tracker.Status, tracker.LastReadChapter, tracker.Rating, tracker.LatestKnownChapter, tracker.LatestReleaseAt, tracker.LatestKnownChapter, tracker.LastCheckedAt, tracker.LastReadChapter)
	if err != nil {
		return nil, fmt.Errorf("insert tracker: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get tracker last insert id: %w", err)
	}

	if err := replaceTrackerSourcesInTx(ctx, tx, tracker.ProfileID, id, []models.TrackerSource{{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}}); err != nil {
		return nil, fmt.Errorf("create tracker sources: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create tracker tx: %w", err)
	}

	return r.GetByIDContext(ctx, tracker.ProfileID, id)
}

func (r *TrackerRepository) GetByID(profileID int64, id int64) (*models.Tracker, error) {
	return r.GetByIDContext(context.Background(), profileID, id)
}

func (r *TrackerRepository) GetByIDContext(ctx context.Context, profileID int64, id int64) (*models.Tracker, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT
			id, profile_id, title, related_titles, source_id, source_item_id, source_url, status,
			last_read_chapter, rating, last_read_at, latest_known_chapter, latest_release_at,
			latest_chapter_seen_at, latest_chapter_source_id, last_checked_at, reading_source_id,
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

	tagsByTracker, err := r.ListTagsByTrackerIDsContext(ctx, profileID, []int64{tracker.ID})
	if err != nil {
		return nil, fmt.Errorf("get tracker tags: %w", err)
	}
	tracker.Tags = tagsByTracker[tracker.ID]

	return tracker, nil
}

func (r *TrackerRepository) Update(profileID int64, id int64, tracker *models.Tracker) (*models.Tracker, error) {
	return r.UpdateContext(context.Background(), profileID, id, tracker)
}

func (r *TrackerRepository) UpdateContext(ctx context.Context, profileID int64, id int64, tracker *models.Tracker) (*models.Tracker, error) {
	relatedTitlesJSON := encodeRelatedTitlesJSON(tracker.RelatedTitles)
	result, err := r.db.ExecContext(ctx, `
		UPDATE trackers
		SET
			title = ?,
			related_titles = ?,
			source_id = ?,
			source_item_id = ?,
			source_url = ?,
			status = ?,
			last_read_chapter = ?,
			rating = ?,
			last_read_at = CASE WHEN last_read_chapter IS NOT ? THEN CURRENT_TIMESTAMP ELSE last_read_at END,
			latest_chapter_seen_at = CASE
				WHEN ? IS NULL THEN NULL
				WHEN latest_known_chapter IS NOT ? THEN CURRENT_TIMESTAMP
				ELSE COALESCE(latest_chapter_seen_at, CURRENT_TIMESTAMP)
			END,
			latest_known_chapter = ?,
			latest_release_at = ?,
			last_checked_at = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND profile_id = ?
		  AND (
			title IS NOT ?
			OR related_titles IS NOT ?
			OR source_id IS NOT ?
			OR source_item_id IS NOT ?
			OR source_url IS NOT ?
			OR status IS NOT ?
			OR last_read_chapter IS NOT ?
			OR rating IS NOT ?
			OR latest_known_chapter IS NOT ?
			OR latest_release_at IS NOT ?
			OR last_checked_at IS NOT ?
		  )
	`,
		tracker.Title,
		relatedTitlesJSON,
		tracker.SourceID,
		tracker.SourceItemID,
		tracker.SourceURL,
		tracker.Status,
		tracker.LastReadChapter,
		tracker.Rating,
		tracker.LastReadChapter,
		tracker.LatestKnownChapter,
		tracker.LatestKnownChapter,
		tracker.LatestKnownChapter,
		tracker.LatestReleaseAt,
		tracker.LastCheckedAt,
		id,
		profileID,
		tracker.Title,
		relatedTitlesJSON,
		tracker.SourceID,
		tracker.SourceItemID,
		tracker.SourceURL,
		tracker.Status,
		tracker.LastReadChapter,
		tracker.Rating,
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
		return r.GetByIDContext(ctx, profileID, id)
	}

	if err := r.UpsertTrackerSourceContext(ctx, profileID, id, models.TrackerSource{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}); err != nil {
		return nil, fmt.Errorf("upsert primary tracker source: %w", err)
	}

	return r.GetByIDContext(ctx, profileID, id)
}

// SetReadingSource pins (or, with nil, unpins) the source a tracker's reading
// links prefer. A non-nil id must belong to one of the tracker's linked
// sources or its primary; anything else clears the preference rather than
// storing a dangling pointer.
func (r *TrackerRepository) SetReadingSource(profileID int64, id int64, readingSourceID *int64) error {
	return r.SetReadingSourceContext(context.Background(), profileID, id, readingSourceID)
}

func (r *TrackerRepository) SetReadingSourceContext(ctx context.Context, profileID int64, id int64, readingSourceID *int64) error {
	if readingSourceID != nil {
		var linked int
		err := r.db.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM trackers t
			WHERE t.id = ? AND t.profile_id = ?
			  AND (t.source_id = ? OR EXISTS (
			      SELECT 1 FROM tracker_sources ts
			      WHERE ts.tracker_id = t.id AND ts.source_id = ?
			  ))
		`, id, profileID, *readingSourceID, *readingSourceID).Scan(&linked)
		if err != nil {
			return fmt.Errorf("validate reading source: %w", err)
		}
		if linked == 0 {
			readingSourceID = nil
		}
	}

	if _, err := r.db.ExecContext(ctx, `
		UPDATE trackers
		SET reading_source_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND profile_id = ? AND reading_source_id IS NOT ?
	`, readingSourceID, id, profileID, readingSourceID); err != nil {
		return fmt.Errorf("set reading source: %w", err)
	}
	return nil
}

func (r *TrackerRepository) UpdateLastReadChapter(profileID int64, id int64, lastReadChapter *float64) (bool, error) {
	return r.UpdateLastReadChapterContext(context.Background(), profileID, id, lastReadChapter)
}

func (r *TrackerRepository) UpdateLastReadChapterContext(ctx context.Context, profileID int64, id int64, lastReadChapter *float64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
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

func (r *TrackerRepository) UpdateRating(profileID int64, id int64, rating *float64) (bool, error) {
	return r.UpdateRatingContext(context.Background(), profileID, id, rating)
}

func (r *TrackerRepository) UpdateRatingContext(ctx context.Context, profileID int64, id int64, rating *float64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE trackers
		SET
			rating = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND profile_id = ?
		  AND rating IS NOT ?
	`, rating, id, profileID, rating)
	if err != nil {
		return false, fmt.Errorf("update rating: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rating update rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

func (r *TrackerRepository) Delete(profileID int64, id int64) (bool, error) {
	return r.DeleteContext(context.Background(), profileID, id)
}

func (r *TrackerRepository) DeleteContext(ctx context.Context, profileID int64, id int64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM trackers WHERE id = ? AND profile_id = ?`, id, profileID)
	if err != nil {
		return false, fmt.Errorf("delete tracker: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("tracker delete rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}
