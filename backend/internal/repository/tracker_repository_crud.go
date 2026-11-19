package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) SourceExists(ctx context.Context, sourceID int64) (bool, error) {
	var count int
	err := r.rdb.QueryRowContext(ctx, `SELECT COUNT(1) FROM sources WHERE id = ?`, sourceID).Scan(&count)
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
	row := r.rdb.QueryRowContext(ctx, `SELECT `+trackerColumns+`
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
//
// When the update moves the primary — to another site, or to another series
// on the same one — the mirror row of the primary it replaces is removed. It
// used to be left behind, and a mirror row that no longer matches the primary
// is by definition an alternate: the tracker went on being polled on the very
// link the caller had just taken it off, as a fallback in good standing (the
// row carried the old primary's in_step), and the usual reason to repoint a
// tracker is that the old link was the wrong series. This call has no list of
// links to say the old one should stay; a caller that wants it kept links it
// again, as the edit form's list does.
func (r *TrackerRepository) Update(ctx context.Context, profileID int64, id int64, tracker *models.Tracker) (*models.Tracker, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update tracker tx: %w", err)
	}
	defer tx.Rollback()

	var previousSourceID int64
	var previousSourceURL string
	err = tx.QueryRowContext(ctx, `
		SELECT source_id, source_url FROM trackers WHERE id = ? AND profile_id = ?
	`, id, profileID).Scan(&previousSourceID, &previousSourceURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tracker before update: %w", err)
	}

	changed, err := updateTrackerRowTx(ctx, tx, profileID, id, tracker)
	if err != nil {
		return nil, err
	}
	if changed {
		primaryMoved := previousSourceID != tracker.SourceID ||
			!strings.EqualFold(strings.TrimSpace(previousSourceURL), strings.TrimSpace(tracker.SourceURL))
		if primaryMoved {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM tracker_sources
				WHERE tracker_id = ? AND source_id = ? AND LOWER(TRIM(source_url)) = LOWER(TRIM(?))
			`, id, previousSourceID, previousSourceURL); err != nil {
				return nil, fmt.Errorf("remove the previous primary's mirror: %w", err)
			}
			// A reading pin on the site just unlinked would be the dangling
			// pointer SetReadingSource refuses to store.
			if _, err := tx.ExecContext(ctx, `
				UPDATE trackers
				SET reading_source_id = NULL
				WHERE id = ?
				  AND reading_source_id IS NOT NULL
				  AND reading_source_id <> source_id
				  AND NOT EXISTS (
				      SELECT 1 FROM tracker_sources ts
				      WHERE ts.tracker_id = trackers.id AND ts.source_id = trackers.reading_source_id
				  )
			`, id); err != nil {
				return nil, fmt.Errorf("clear a reading pin left without its link: %w", err)
			}
		}
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
