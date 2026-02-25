package repository

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) ListProfileTags(profileID int64) ([]models.CustomTag, error) {
	rows, err := r.db.Query(`
		SELECT id, profile_id, name, icon_key, created_at, updated_at
		FROM custom_tags
		WHERE profile_id = ?
		ORDER BY
			CASE
				WHEN TRIM(COALESCE(icon_key, '')) IN ('icon_1', 'icon_2', 'icon_3') THEN 0
				ELSE 1
			END ASC,
			name ASC,
			id ASC
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile tags: %w", err)
	}
	defer rows.Close()

	items := make([]models.CustomTag, 0)
	for rows.Next() {
		var item models.CustomTag
		var iconKey sql.NullString
		if err := rows.Scan(&item.ID, &item.ProfileID, &item.Name, &iconKey, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan profile tag: %w", err)
		}
		if iconKey.Valid {
			iconValue := strings.TrimSpace(iconKey.String)
			if iconValue != "" {
				item.IconKey = &iconValue
				item.IconPath = iconPathFromKey(iconValue)
			}
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile tags: %w", err)
	}

	return items, nil
}

func (r *TrackerRepository) UpsertProfileTag(profileID int64, name string, iconKey *string) (*models.CustomTag, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, fmt.Errorf("tag name is required")
	}

	var normalizedIconKey any
	if iconKey != nil {
		trimmedIcon := strings.TrimSpace(*iconKey)
		if trimmedIcon != "" {
			normalizedIconKey = trimmedIcon
		}
	}

	_, err := r.db.Exec(`
		INSERT INTO custom_tags (profile_id, name, icon_key)
		VALUES (?, ?, ?)
		ON CONFLICT(profile_id, name)
		DO UPDATE SET
			icon_key = excluded.icon_key,
			updated_at = CURRENT_TIMESTAMP
	`, profileID, trimmedName, normalizedIconKey)
	if err != nil {
		return nil, fmt.Errorf("upsert profile tag: %w", err)
	}

	row := r.db.QueryRow(`
		SELECT id, profile_id, name, icon_key, created_at, updated_at
		FROM custom_tags
		WHERE profile_id = ? AND name = ?
	`, profileID, trimmedName)

	var tag models.CustomTag
	var storedIcon sql.NullString
	if err := row.Scan(&tag.ID, &tag.ProfileID, &tag.Name, &storedIcon, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
		return nil, fmt.Errorf("get upserted profile tag: %w", err)
	}

	if storedIcon.Valid {
		iconValue := strings.TrimSpace(storedIcon.String)
		if iconValue != "" {
			tag.IconKey = &iconValue
			tag.IconPath = iconPathFromKey(iconValue)
		}
	}

	return &tag, nil
}

func (r *TrackerRepository) CreateProfileTag(profileID int64, name string, iconKey *string) (*models.CustomTag, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, fmt.Errorf("tag name is required")
	}

	var normalizedIconKey any
	if iconKey != nil {
		trimmedIcon := strings.TrimSpace(*iconKey)
		if trimmedIcon != "" {
			normalizedIconKey = trimmedIcon
		}
	}

	result, err := r.db.Exec(`
		INSERT INTO custom_tags (profile_id, name, icon_key)
		VALUES (?, ?, ?)
	`, profileID, trimmedName, normalizedIconKey)
	if err != nil {
		return nil, fmt.Errorf("create profile tag: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get created profile tag id: %w", err)
	}

	row := r.db.QueryRow(`
		SELECT id, profile_id, name, icon_key, created_at, updated_at
		FROM custom_tags
		WHERE id = ? AND profile_id = ?
	`, id, profileID)

	var tag models.CustomTag
	var storedIcon sql.NullString
	if err := row.Scan(&tag.ID, &tag.ProfileID, &tag.Name, &storedIcon, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
		return nil, fmt.Errorf("get created profile tag: %w", err)
	}

	if storedIcon.Valid {
		iconValue := strings.TrimSpace(storedIcon.String)
		if iconValue != "" {
			tag.IconKey = &iconValue
			tag.IconPath = iconPathFromKey(iconValue)
		}
	}

	return &tag, nil
}

func (r *TrackerRepository) RenameProfileTag(profileID int64, tagID int64, name string) (bool, error) {
	if tagID <= 0 {
		return false, nil
	}

	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return false, fmt.Errorf("tag name is required")
	}

	result, err := r.db.Exec(`
		UPDATE custom_tags
		SET name = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND profile_id = ?
	`, trimmedName, tagID, profileID)
	if err != nil {
		return false, fmt.Errorf("rename profile tag: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("profile tag rename rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

func (r *TrackerRepository) DeleteProfileTag(profileID int64, tagID int64) (bool, error) {
	if tagID <= 0 {
		return false, nil
	}

	result, err := r.db.Exec(`DELETE FROM custom_tags WHERE id = ? AND profile_id = ?`, tagID, profileID)
	if err != nil {
		return false, fmt.Errorf("delete profile tag: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("profile tag delete rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

func (r *TrackerRepository) ReplaceTrackerTags(profileID int64, trackerID int64, tagIDs []int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replace tracker tags tx: %w", err)
	}

	var trackerExists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM trackers WHERE id = ? AND profile_id = ?`, trackerID, profileID).Scan(&trackerExists); err != nil {
		tx.Rollback()
		return fmt.Errorf("check tracker ownership for tags: %w", err)
	}
	if trackerExists == 0 {
		tx.Rollback()
		return nil
	}

	if _, err := tx.Exec(`DELETE FROM tracker_tags WHERE tracker_id = ?`, trackerID); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete tracker tags: %w", err)
	}

	uniqueTagIDs := dedupePositiveInt64(tagIDs)
	if len(uniqueTagIDs) > 0 {
		lookupArgs := make([]any, 0, len(uniqueTagIDs)+1)
		lookupArgs = append(lookupArgs, profileID)
		for _, tagID := range uniqueTagIDs {
			lookupArgs = append(lookupArgs, tagID)
		}

		rows, err := tx.Query(`
			SELECT id
			FROM custom_tags
			WHERE profile_id = ?
			  AND id IN (`+sqlPlaceholders(len(uniqueTagIDs))+`)
		`, lookupArgs...)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("lookup profile tags: %w", err)
		}

		validTagIDs := make(map[int64]struct{}, len(uniqueTagIDs))
		for rows.Next() {
			var tagID int64
			if err := rows.Scan(&tagID); err != nil {
				rows.Close()
				tx.Rollback()
				return fmt.Errorf("scan profile tag id: %w", err)
			}
			validTagIDs[tagID] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			tx.Rollback()
			return fmt.Errorf("iterate profile tag ids: %w", err)
		}
		rows.Close()

		insertStmt, err := tx.Prepare(`INSERT INTO tracker_tags (tracker_id, tag_id) VALUES (?, ?)`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("prepare tracker tag insert: %w", err)
		}
		defer insertStmt.Close()

		for _, tagID := range uniqueTagIDs {
			if _, exists := validTagIDs[tagID]; !exists {
				continue
			}

			if _, err := insertStmt.Exec(trackerID, tagID); err != nil {
				tx.Rollback()
				return fmt.Errorf("insert tracker tag: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace tracker tags tx: %w", err)
	}

	return nil
}

func (r *TrackerRepository) ListTagsByTrackerIDs(profileID int64, trackerIDs []int64) (map[int64][]models.CustomTag, error) {
	result := make(map[int64][]models.CustomTag, len(trackerIDs))
	if len(trackerIDs) == 0 {
		return result, nil
	}

	uniqueTrackerIDs := dedupePositiveInt64(trackerIDs)
	if len(uniqueTrackerIDs) == 0 {
		return result, nil
	}

	args := make([]any, 0, len(uniqueTrackerIDs)+1)
	args = append(args, profileID)
	for _, id := range uniqueTrackerIDs {
		args = append(args, id)
	}

	query := `
		SELECT
			tt.tracker_id,
			ct.id,
			ct.profile_id,
			ct.name,
			ct.icon_key,
			ct.created_at,
			ct.updated_at
		FROM tracker_tags tt
		INNER JOIN custom_tags ct ON ct.id = tt.tag_id
		WHERE ct.profile_id = ?
		  AND tt.tracker_id IN (` + sqlPlaceholders(len(uniqueTrackerIDs)) + `)
		ORDER BY tt.tracker_id ASC, ct.name ASC, ct.id ASC
	`

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags by tracker ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var trackerID int64
		var tag models.CustomTag
		var iconKey sql.NullString
		if err := rows.Scan(&trackerID, &tag.ID, &tag.ProfileID, &tag.Name, &iconKey, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan tracker tag row: %w", err)
		}
		if iconKey.Valid {
			iconValue := strings.TrimSpace(iconKey.String)
			if iconValue != "" {
				tag.IconKey = &iconValue
				tag.IconPath = iconPathFromKey(iconValue)
			}
		}
		result[trackerID] = append(result[trackerID], tag)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker tags rows: %w", err)
	}

	return result, nil
}

func trackerIDs(trackers []models.Tracker) []int64 {
	ids := make([]int64, 0, len(trackers))
	for _, tracker := range trackers {
		ids = append(ids, tracker.ID)
	}
	return ids
}

func iconPathFromKey(iconKey string) *string {
	switch strings.TrimSpace(iconKey) {
	case "icon_1":
		path := "/assets/tag-icons/icon-star-gold.svg"
		return &path
	case "icon_2":
		path := "/assets/tag-icons/icon-red-heart.svg"
		return &path
	case "icon_3":
		path := "/assets/tag-icons/icon-flames.svg"
		return &path
	default:
		return nil
	}
}

func dedupePositiveInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
