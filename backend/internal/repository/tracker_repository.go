package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type TrackerListOptions struct {
	Statuses []string
	SortBy   string
	Order    string
	Query    string
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
			title, source_id, source_item_id, source_url, status, last_read_chapter, latest_known_chapter, latest_release_at, last_checked_at, last_read_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)
	`, tracker.Title, tracker.SourceID, tracker.SourceItemID, tracker.SourceURL, tracker.Status, tracker.LastReadChapter, tracker.LatestKnownChapter, tracker.LatestReleaseAt, tracker.LastCheckedAt, tracker.LastReadChapter)
	if err != nil {
		return nil, fmt.Errorf("insert tracker: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get tracker last insert id: %w", err)
	}

	if err := r.ReplaceTrackerSources(id, []models.TrackerSource{{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}}); err != nil {
		return nil, fmt.Errorf("create tracker sources: %w", err)
	}

	return r.GetByID(id)
}

func (r *TrackerRepository) GetByID(id int64) (*models.Tracker, error) {
	row := r.db.QueryRow(`
		SELECT
			id, title, source_id, source_item_id, source_url, status,
			last_read_chapter, last_read_at, latest_known_chapter, latest_release_at, last_checked_at,
			created_at, updated_at
		FROM trackers
		WHERE id = ?
	`, id)

	tracker, err := scanTracker(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get tracker by id: %w", err)
	}

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
			id, title, source_id, source_item_id, source_url, status,
			last_read_chapter, last_read_at, latest_known_chapter, latest_release_at, last_checked_at,
			created_at, updated_at
		FROM trackers
	`

	args := make([]any, 0)
	whereClauses := make([]string, 0)

	if strings.TrimSpace(options.Query) != "" {
		whereClauses = append(whereClauses, `LOWER(title) LIKE ?`)
		queryLike := "%" + strings.ToLower(strings.TrimSpace(options.Query)) + "%"
		args = append(args, queryLike)
	}

	if len(options.Statuses) > 0 {
		placeholders := make([]string, 0, len(options.Statuses))
		for _, status := range options.Statuses {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		whereClauses = append(whereClauses, `status IN (`+strings.Join(placeholders, ",")+`)`)
	}

	if len(whereClauses) > 0 {
		query += ` WHERE ` + strings.Join(whereClauses, " AND ")
	}

	query += ` ORDER BY ` + sortField + ` ` + order + `, id DESC`

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

	return trackers, nil
}

func (r *TrackerRepository) Update(id int64, tracker *models.Tracker) (*models.Tracker, error) {
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
		return r.GetByID(id)
	}

	if err := r.UpsertTrackerSource(id, models.TrackerSource{
		SourceID:     tracker.SourceID,
		SourceItemID: tracker.SourceItemID,
		SourceURL:    tracker.SourceURL,
	}); err != nil {
		return nil, fmt.Errorf("upsert primary tracker source: %w", err)
	}

	return r.GetByID(id)
}

func (r *TrackerRepository) UpdateLastReadChapter(id int64, lastReadChapter *float64) (bool, error) {
	result, err := r.db.Exec(`
		UPDATE trackers
		SET
			last_read_chapter = ?,
			last_read_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND last_read_chapter IS NOT ?
	`, lastReadChapter, id, lastReadChapter)
	if err != nil {
		return false, fmt.Errorf("update last read chapter: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("last read update rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

func (r *TrackerRepository) ListTrackerSources(trackerID int64) ([]models.TrackerSource, error) {
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
		INNER JOIN sources s ON s.id = ts.source_id
		WHERE ts.tracker_id = ?
		ORDER BY s.name ASC, ts.id ASC
	`, trackerID)
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

func (r *TrackerRepository) ReplaceTrackerSources(trackerID int64, sources []models.TrackerSource) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replace tracker sources tx: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM tracker_sources WHERE tracker_id = ?`, trackerID); err != nil {
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

func (r *TrackerRepository) UpsertTrackerSource(trackerID int64, source models.TrackerSource) error {
	if source.SourceID <= 0 || strings.TrimSpace(source.SourceURL) == "" {
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

func (r *TrackerRepository) Delete(id int64) (bool, error) {
	result, err := r.db.Exec(`DELETE FROM trackers WHERE id = ?`, id)
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
