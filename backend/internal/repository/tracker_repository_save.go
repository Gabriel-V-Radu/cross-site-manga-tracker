package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

// TrackerLinks is everything a save attaches to a tracker besides its own row:
// the linked sources, the tags, an extra pasted link and the reading pin. The
// edit form used to write these through five separate methods, each its own
// transaction and its own ownership check, so a failure at the third left the
// title and sources saved and the tags and pin not. SaveTrackerEdit and
// CreateWithLinks take the whole set and commit once.
type TrackerLinks struct {
	// Sources replaces the tracker's linked sources wholesale when non-nil. The
	// primary source is mirrored in afterwards either way, so it can never be
	// unlinked by a list that forgot it.
	Sources []models.TrackerSource
	// TagIDs replaces the tracker's tags when non-nil; an empty slice clears
	// them. Ids the profile does not own are dropped, not stored.
	TagIDs []int64
	// ExtraSource is upserted after Sources is applied — the "link another site
	// by URL" field — so it survives the replace.
	ExtraSource *models.TrackerSource
	// ReadingSourceID pins the reading site when SetReadingSource is true; nil
	// clears the pin. A pin to a source the tracker is not linked to is
	// cleared rather than stored dangling.
	SetReadingSource bool
	ReadingSourceID  *int64
}

// CreateWithLinks inserts a tracker with its primary source mirror, tags and
// optional extra link in one transaction. Create is the same with no links.
func (r *TrackerRepository) CreateWithLinks(ctx context.Context, tracker *models.Tracker, links TrackerLinks) (*models.Tracker, error) {
	// With MaxOpenConns(1) everything up to Commit must run on the tx handle —
	// including GetByID staying AFTER the commit, since it would otherwise wait
	// on the connection this tx holds.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create tracker tx: %w", err)
	}
	defer tx.Rollback()

	id, err := insertTrackerTx(ctx, tx, tracker)
	if err != nil {
		return nil, err
	}

	if err := replaceTrackerSourcesInTx(ctx, tx, tracker.ProfileID, id, []models.TrackerSource{primarySourceOf(tracker)}); err != nil {
		return nil, fmt.Errorf("create tracker sources: %w", err)
	}
	if links.ExtraSource != nil {
		if err := upsertTrackerSourceTx(ctx, tx, id, *links.ExtraSource); err != nil {
			return nil, fmt.Errorf("link extra source: %w", err)
		}
	}
	if links.TagIDs != nil {
		if err := replaceTrackerTagsInTx(ctx, tx, tracker.ProfileID, id, links.TagIDs); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create tracker tx: %w", err)
	}

	return r.GetByID(ctx, tracker.ProfileID, id)
}

// SaveTrackerEdit applies an edit-form save as one write: the tracker row, the
// linked sources, the tags, the pasted link and the reading pin. It returns nil
// when the tracker does not belong to the profile, and nothing is written in
// that case either.
func (r *TrackerRepository) SaveTrackerEdit(ctx context.Context, profileID int64, id int64, tracker *models.Tracker, links TrackerLinks) (*models.Tracker, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin save tracker tx: %w", err)
	}
	defer tx.Rollback()

	owned, err := trackerOwnedByProfileTx(ctx, tx, id, profileID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}

	if _, err := updateTrackerRowTx(ctx, tx, profileID, id, tracker); err != nil {
		return nil, err
	}

	if links.Sources != nil {
		if err := replaceTrackerSourcesInTx(ctx, tx, profileID, id, links.Sources); err != nil {
			return nil, err
		}
	}
	// The primary is always linked: a save that changed it, or a list that left
	// it out, would otherwise leave the tracker's own source without a mirror
	// row until the next poll put one back.
	if err := upsertTrackerSourceTx(ctx, tx, id, primarySourceOf(tracker)); err != nil {
		return nil, fmt.Errorf("mirror primary source: %w", err)
	}
	if links.ExtraSource != nil {
		if err := upsertTrackerSourceTx(ctx, tx, id, *links.ExtraSource); err != nil {
			return nil, fmt.Errorf("link extra source: %w", err)
		}
	}
	if links.TagIDs != nil {
		if err := replaceTrackerTagsInTx(ctx, tx, profileID, id, links.TagIDs); err != nil {
			return nil, err
		}
	}
	if links.SetReadingSource {
		if err := setReadingSourceTx(ctx, tx, profileID, id, links.ReadingSourceID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit save tracker tx: %w", err)
	}

	return r.GetByID(ctx, profileID, id)
}

func primarySourceOf(tracker *models.Tracker) models.TrackerSource {
	return models.TrackerSource{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}
}

// insertTrackerTx writes the tracker row and returns its id.
func insertTrackerTx(ctx context.Context, tx *sql.Tx, tracker *models.Tracker) (int64, error) {
	relatedTitlesJSON := encodeRelatedTitlesJSON(tracker.RelatedTitles)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO trackers (
			profile_id, title, related_titles, source_id, source_item_id, source_url, status, last_read_chapter, rating, latest_known_chapter, latest_release_at, latest_chapter_seen_at, last_checked_at, last_read_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END, ?, CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)
	`, tracker.ProfileID, tracker.Title, relatedTitlesJSON, tracker.SourceID, tracker.SourceItemID, tracker.SourceURL, tracker.Status, tracker.LastReadChapter, tracker.Rating, tracker.LatestKnownChapter, tracker.LatestReleaseAt, tracker.LatestKnownChapter, tracker.LastCheckedAt, tracker.LastReadChapter)
	if err != nil {
		return 0, fmt.Errorf("insert tracker: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get tracker last insert id: %w", err)
	}
	return id, nil
}

// updateTrackerRowTx writes the tracker's own columns and reports whether any
// of them changed. Every expression is evaluated against the row as it stands,
// which is what lets last_read_at and latest_chapter_seen_at restamp only when
// their value moved.
func updateTrackerRowTx(ctx context.Context, tx *sql.Tx, profileID int64, id int64, tracker *models.Tracker) (bool, error) {
	relatedTitlesJSON := encodeRelatedTitlesJSON(tracker.RelatedTitles)
	result, err := tx.ExecContext(ctx, `
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
		return false, fmt.Errorf("update tracker: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("tracker update rows affected: %w", err)
	}
	return rowsAffected > 0, nil
}

// replaceTrackerTagsInTx is ReplaceTrackerTags' body on a caller-owned
// transaction. A tracker the profile does not own is left alone.
func replaceTrackerTagsInTx(ctx context.Context, tx *sql.Tx, profileID int64, trackerID int64, tagIDs []int64) error {
	owned, err := trackerOwnedByProfileTx(ctx, tx, trackerID, profileID)
	if err != nil {
		return fmt.Errorf("check tracker ownership for tags: %w", err)
	}
	if !owned {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM tracker_tags WHERE tracker_id = ?`, trackerID); err != nil {
		return fmt.Errorf("delete tracker tags: %w", err)
	}

	uniqueTagIDs := dedupePositiveInt64(tagIDs)
	if len(uniqueTagIDs) == 0 {
		return nil
	}

	validTagIDs, err := profileTagIDsInTx(ctx, tx, profileID, uniqueTagIDs)
	if err != nil {
		return err
	}

	insertStmt, err := tx.PrepareContext(ctx, `INSERT INTO tracker_tags (tracker_id, tag_id) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare tracker tag insert: %w", err)
	}
	defer insertStmt.Close()

	for _, tagID := range uniqueTagIDs {
		if _, exists := validTagIDs[tagID]; !exists {
			continue
		}
		if _, err := insertStmt.ExecContext(ctx, trackerID, tagID); err != nil {
			return fmt.Errorf("insert tracker tag: %w", err)
		}
	}
	return nil
}

// setReadingSourceTx is SetReadingSource's body on a caller-owned transaction.
// A non-nil id must be the tracker's primary or one of its linked sources;
// anything else clears the pin rather than storing a dangling pointer.
func setReadingSourceTx(ctx context.Context, tx *sql.Tx, profileID int64, id int64, readingSourceID *int64) error {
	if readingSourceID != nil {
		var linked int
		err := tx.QueryRowContext(ctx, `
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

	if _, err := tx.ExecContext(ctx, `
		UPDATE trackers
		SET reading_source_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND profile_id = ? AND reading_source_id IS NOT ?
	`, readingSourceID, id, profileID, readingSourceID); err != nil {
		return fmt.Errorf("set reading source: %w", err)
	}
	return nil
}
