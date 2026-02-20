package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

func (r *TrackerRepository) List(options TrackerListOptions) ([]models.Tracker, error) {
	validSortFields := map[string]string{
		"title":                "title",
		"created_at":           "created_at",
		"updated_at":           "updated_at",
		"last_read_at":         "last_read_at",
		"last_checked_at":      "last_checked_at",
		"rating":               "rating",
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
			last_read_chapter, rating, last_read_at, latest_known_chapter, latest_release_at, last_checked_at,
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

	if len(options.SourceIDs) > 0 {
		seenSourceIDs := make(map[int64]bool, len(options.SourceIDs))
		filteredSourceIDs := make([]int64, 0, len(options.SourceIDs))
		for _, sourceID := range options.SourceIDs {
			if sourceID <= 0 || seenSourceIDs[sourceID] {
				continue
			}
			seenSourceIDs[sourceID] = true
			filteredSourceIDs = append(filteredSourceIDs, sourceID)
		}

		if len(filteredSourceIDs) > 0 {
			placeholders := make([]string, 0, len(filteredSourceIDs))
			for range filteredSourceIDs {
				placeholders = append(placeholders, "?")
			}
			joinedPlaceholders := strings.Join(placeholders, ",")

			whereClauses = append(whereClauses, `(trackers.source_id IN (`+joinedPlaceholders+`) OR EXISTS (
				SELECT 1
				FROM tracker_sources ts
				WHERE ts.tracker_id = trackers.id
				  AND ts.source_id IN (`+joinedPlaceholders+`)
			))`)
			for _, sourceID := range filteredSourceIDs {
				args = append(args, sourceID)
			}
			for _, sourceID := range filteredSourceIDs {
				args = append(args, sourceID)
			}
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
