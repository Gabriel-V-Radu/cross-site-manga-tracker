package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type TrackerListOptions struct {
	ProfileID int64
	Statuses  []string
	TagNames  []string
	SortBy    string
	Order     string
	Query     string
	Limit     int
	Offset    int
}

type TrackerRepository struct {
	db *sql.DB
}

type PollingTracker struct {
	ID                 int64
	Title              string
	Status             string
	SourceURL          string
	LatestKnownChapter *float64
	SourceKey          string
}

func NewTrackerRepository(db *sql.DB) *TrackerRepository {
	return &TrackerRepository{db: db}
}

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

func (r *TrackerRepository) List(options TrackerListOptions) ([]models.Tracker, error) {
	validSortFields := map[string]string{
		"title":                "title",
		"created_at":           "created_at",
		"updated_at":           "updated_at",
		"last_read_at":         "last_read_at",
		"last_checked_at":      "last_checked_at",
		"latest_known_chapter": "CASE WHEN latest_known_chapter IS NULL THEN NULL ELSE COALESCE(latest_release_at, last_checked_at, updated_at, created_at) END",
	}
	sortField, ok := validSortFields[options.SortBy]
	if !ok {
		sortField = validSortFields["latest_known_chapter"]
	}

	order := strings.ToUpper(options.Order)
	if order != "ASC" && order != "DESC" {
		order = "DESC"
	}

	query := `
		SELECT
			id, profile_id, title, source_id, source_item_id, source_url, status,
			last_read_chapter, last_read_at, latest_known_chapter, latest_release_at, last_checked_at,
			created_at, updated_at
		FROM trackers
	`

	whereClauses, args := buildTrackerListFilters(options)

	if len(whereClauses) > 0 {
		query += ` WHERE ` + strings.Join(whereClauses, " AND ")
	}

	query += ` ORDER BY ` + sortField + ` ` + order + `, id DESC`

	if options.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, options.Limit)
		if options.Offset > 0 {
			query += ` OFFSET ?`
			args = append(args, options.Offset)
		}
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list trackers: %w", err)
	}
	defer rows.Close()

	trackers := make([]models.Tracker, 0)
	for rows.Next() {
		tracker, err := scanTracker(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tracker row: %w", err)
		}
		trackers = append(trackers, *tracker)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker rows: %w", err)
	}

	if len(trackers) == 0 {
		return trackers, nil
	}

	tagsByTracker, err := r.ListTagsByTrackerIDs(options.ProfileID, trackerIDs(trackers))
	if err != nil {
		return nil, fmt.Errorf("list tracker tags: %w", err)
	}
	for index := range trackers {
		trackers[index].Tags = tagsByTracker[trackers[index].ID]
	}

	return trackers, nil
}

func (r *TrackerRepository) Count(options TrackerListOptions) (int, error) {
	query := `SELECT COUNT(1) FROM trackers`
	whereClauses, args := buildTrackerListFilters(options)
	if len(whereClauses) > 0 {
		query += ` WHERE ` + strings.Join(whereClauses, " AND ")
	}

	var total int
	if err := r.db.QueryRow(query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count trackers: %w", err)
	}

	return total, nil
}

func buildTrackerListFilters(options TrackerListOptions) ([]string, []any) {
	args := make([]any, 0, 1)
	whereClauses := make([]string, 0, 1)

	whereClauses = append(whereClauses, `profile_id = ?`)
	args = append(args, options.ProfileID)

	if strings.TrimSpace(options.Query) != "" {
		whereClauses = append(whereClauses, `LOWER(title) LIKE ?`)
		queryLike := "%" + strings.ToLower(strings.TrimSpace(options.Query)) + "%"
		args = append(args, queryLike)
	}

	if len(options.Statuses) > 0 {
		placeholders := make([]string, 0, len(options.Statuses))
		hasReading := false
		for _, status := range options.Statuses {
			if strings.EqualFold(strings.TrimSpace(status), "reading") {
				hasReading = true
			}
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		whereClauses = append(whereClauses, `status IN (`+strings.Join(placeholders, ",")+`)`)
		if hasReading {
			whereClauses = append(whereClauses, `(status <> 'reading' OR latest_known_chapter IS NULL OR last_read_chapter IS NULL OR last_read_chapter < latest_known_chapter)`)
		}
	}

	if len(options.TagNames) > 0 {
		for _, tagName := range options.TagNames {
			normalized := strings.TrimSpace(strings.ToLower(tagName))
			if normalized == "" {
				continue
			}
			whereClauses = append(whereClauses, `EXISTS (
				SELECT 1
				FROM tracker_tags tt
				INNER JOIN custom_tags ct ON ct.id = tt.tag_id
				WHERE tt.tracker_id = trackers.id
				  AND ct.profile_id = ?
				  AND LOWER(ct.name) = ?
			)`)
			args = append(args, options.ProfileID, normalized)
		}
	}

	return whereClauses, args
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

func (r *TrackerRepository) ListForPolling(statuses []string) ([]PollingTracker, error) {
	query := `
		SELECT
			t.id, t.title, t.status, t.source_url, t.latest_known_chapter, s.key
		FROM trackers t
		INNER JOIN sources s ON s.id = t.source_id
	`

	args := make([]any, 0)
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		query += ` WHERE t.status IN (` + strings.Join(placeholders, ",") + `)`
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list trackers for polling: %w", err)
	}
	defer rows.Close()

	items := make([]PollingTracker, 0)
	for rows.Next() {
		var item PollingTracker
		var latest sql.NullFloat64
		if err := rows.Scan(&item.ID, &item.Title, &item.Status, &item.SourceURL, &latest, &item.SourceKey); err != nil {
			return nil, fmt.Errorf("scan polling tracker: %w", err)
		}
		if latest.Valid {
			item.LatestKnownChapter = &latest.Float64
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate polling trackers: %w", err)
	}

	return items, nil
}

func (r *TrackerRepository) UpdatePollingState(id int64, latestKnownChapter *float64, latestReleaseAt *time.Time, checkedAt time.Time) error {
	var latestReleaseValue any
	if latestReleaseAt != nil {
		latestReleaseValue = latestReleaseAt.UTC()
	}

	_, err := r.db.Exec(`
		UPDATE trackers
		SET latest_known_chapter = ?, latest_release_at = COALESCE(?, latest_release_at), last_checked_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, latestKnownChapter, latestReleaseValue, checkedAt.UTC(), id)
	if err != nil {
		return fmt.Errorf("update polling state: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTracker(scanner rowScanner) (*models.Tracker, error) {
	var tracker models.Tracker
	var sourceItemID sql.NullString
	var lastReadChapter sql.NullFloat64
	var lastReadAt sql.NullTime
	var latestKnownChapter sql.NullFloat64
	var latestReleaseAt sql.NullTime
	var lastCheckedAt sql.NullTime

	err := scanner.Scan(
		&tracker.ID,
		&tracker.ProfileID,
		&tracker.Title,
		&tracker.SourceID,
		&sourceItemID,
		&tracker.SourceURL,
		&tracker.Status,
		&lastReadChapter,
		&lastReadAt,
		&latestKnownChapter,
		&latestReleaseAt,
		&lastCheckedAt,
		&tracker.CreatedAt,
		&tracker.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if sourceItemID.Valid {
		tracker.SourceItemID = &sourceItemID.String
	}
	if lastReadChapter.Valid {
		tracker.LastReadChapter = &lastReadChapter.Float64
	}
	if lastReadAt.Valid {
		tracker.LastReadAt = &lastReadAt.Time
	}
	if latestKnownChapter.Valid {
		tracker.LatestKnownChapter = &latestKnownChapter.Float64
	}
	if latestReleaseAt.Valid {
		tracker.LatestReleaseAt = &latestReleaseAt.Time
	}
	if lastCheckedAt.Valid {
		tracker.LastCheckedAt = &lastCheckedAt.Time
	}

	return &tracker, nil
}

func (r *TrackerRepository) ListProfileTags(profileID int64) ([]models.CustomTag, error) {
	rows, err := r.db.Query(`
		SELECT id, profile_id, name, icon_key, created_at, updated_at
		FROM custom_tags
		WHERE profile_id = ?
		ORDER BY name ASC, id ASC
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

	for _, tagID := range dedupeInt64(tagIDs) {
		if tagID <= 0 {
			continue
		}

		var tagExists int
		if err := tx.QueryRow(`SELECT COUNT(1) FROM custom_tags WHERE id = ? AND profile_id = ?`, tagID, profileID).Scan(&tagExists); err != nil {
			tx.Rollback()
			return fmt.Errorf("validate profile tag ownership: %w", err)
		}
		if tagExists == 0 {
			continue
		}

		if _, err := tx.Exec(`INSERT INTO tracker_tags (tracker_id, tag_id) VALUES (?, ?)`, trackerID, tagID); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert tracker tag: %w", err)
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

	placeholders := make([]string, 0, len(trackerIDs))
	args := make([]any, 0, len(trackerIDs)+1)
	args = append(args, profileID)
	for _, id := range trackerIDs {
		placeholders = append(placeholders, "?")
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
		  AND tt.tracker_id IN (` + strings.Join(placeholders, ",") + `)
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

func dedupeInt64(values []int64) []int64 {
	seen := make(map[int64]bool, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
