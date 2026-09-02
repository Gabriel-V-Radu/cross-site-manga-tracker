package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) SourceExists(ctx context.Context, sourceID int64) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sources WHERE id = ?`, sourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check source exists: %w", err)
	}
	return count > 0, nil
}

// Create inserts a tracker and its primary source mirror as one write:
// committing the INSERT and then failing to link the source used to leave a
// tracker with no tracker_sources rows behind. The form path, which also has
// tags and a pasted link to attach, uses CreateWithLinks.
func (r *TrackerRepository) Create(ctx context.Context, tracker *models.Tracker) (*models.Tracker, error) {
	return r.CreateWithLinks(ctx, tracker, TrackerLinks{})
}

func (r *TrackerRepository) GetByID(ctx context.Context, profileID int64, id int64) (*models.Tracker, error) {
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

	tagsByTracker, err := r.ListTagsByTrackerIDs(ctx, profileID, []int64{tracker.ID})
	if err != nil {
		return nil, fmt.Errorf("get tracker tags: %w", err)
	}
	tracker.Tags = tagsByTracker[tracker.ID]

	return tracker, nil
}

// Update writes the tracker's own columns and mirrors the (possibly new)
// primary source into tracker_sources, in one transaction — a failure between
// the two used to leave the new primary without a mirror row until the next
// poll. It returns nil for a tracker the profile does not own. The edit form,
// which also replaces sources, tags and the reading pin, uses SaveTrackerEdit.
func (r *TrackerRepository) Update(ctx context.Context, profileID int64, id int64, tracker *models.Tracker) (*models.Tracker, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update tracker tx: %w", err)
	}
	defer tx.Rollback()

	owned, err := trackerOwnedByProfileTx(ctx, tx, id, profileID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}

	changed, err := updateTrackerRowTx(ctx, tx, profileID, id, tracker)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := upsertTrackerSourceTx(ctx, tx, id, primarySourceOf(tracker)); err != nil {
			return nil, fmt.Errorf("upsert primary tracker source: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update tracker tx: %w", err)
	}

	return r.GetByID(ctx, profileID, id)
}

// SetReadingSource pins (or, with nil, unpins) the source a tracker's reading
// links prefer. A non-nil id must belong to one of the tracker's linked
// sources or its primary; anything else clears the preference rather than
// storing a dangling pointer.
func (r *TrackerRepository) SetReadingSource(ctx context.Context, profileID int64, id int64, readingSourceID *int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set reading source tx: %w", err)
	}
	defer tx.Rollback()

	if err := setReadingSourceTx(ctx, tx, profileID, id, readingSourceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set reading source tx: %w", err)
	}
	return nil
}

func (r *TrackerRepository) UpdateLastReadChapter(ctx context.Context, profileID int64, id int64, lastReadChapter *float64) (bool, error) {
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

func (r *TrackerRepository) UpdateRating(ctx context.Context, profileID int64, id int64, rating *float64) (bool, error) {
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

func (r *TrackerRepository) Delete(ctx context.Context, profileID int64, id int64) (bool, error) {
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
