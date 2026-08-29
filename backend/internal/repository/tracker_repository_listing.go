package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

func (r *TrackerRepository) List(options TrackerListOptions) ([]models.Tracker, error) {
	validSortFields := map[string]string{
		"title":           "title",
		"created_at":      "created_at",
		"updated_at":      "updated_at",
		"last_read_at":    "last_read_at",
		"last_checked_at": "last_checked_at",
		"rating":          "rating",
		// "Newest chapter first" must rank by when the chapter appeared, so the
		// fallbacks may only be dates that move when a chapter does.
		// last_checked_at and updated_at were in this chain and both are rewritten
		// by every poll, which ranked a tracker whose source reports no release
		// date by when it was last polled: those trackers were pinned to the top of
		// the library and could never move down, however old the series was.
		"latest_known_chapter": "CASE WHEN latest_known_chapter IS NULL THEN NULL ELSE COALESCE(latest_release_at, latest_chapter_seen_at, created_at) END",
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
			id, profile_id, title, related_titles, source_id, source_item_id, source_url, status,
			last_read_chapter, rating, last_read_at, latest_known_chapter, latest_release_at,
			latest_chapter_seen_at, latest_chapter_source_id, last_checked_at, reading_source_id,
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
		normalizedQuery := searchutil.Normalize(options.Query)
		if normalizedQuery != "" {
			queryTokens := searchutil.TokenizeNormalized(normalizedQuery)
			if len(queryTokens) == 0 {
				whereClauses = append(whereClauses, `(LOWER(trackers.title) LIKE ? OR LOWER(COALESCE(trackers.related_titles, '')) LIKE ?)`)
				queryLike := "%" + normalizedQuery + "%"
				args = append(args, queryLike, queryLike)
			} else {
				for _, token := range queryTokens {
					tokenLike := "%" + token + "%"
					whereClauses = append(whereClauses, `(LOWER(trackers.title) LIKE ? OR LOWER(COALESCE(trackers.related_titles, '')) LIKE ?)`)
					args = append(args, tokenLike, tokenLike)
				}
			}
		}
	}

	if len(options.Statuses) > 0 {
		statuses := make([]string, 0, len(options.Statuses))
		seenStatuses := make(map[string]struct{}, len(options.Statuses))
		hasReading := false
		for _, rawStatus := range options.Statuses {
			status := strings.TrimSpace(rawStatus)
			if status == "" {
				continue
			}

			normalizedStatus := strings.ToLower(status)
			if _, exists := seenStatuses[normalizedStatus]; exists {
				continue
			}
			seenStatuses[normalizedStatus] = struct{}{}
			if normalizedStatus == "reading" {
				hasReading = true
			}
			statuses = append(statuses, status)
		}

		if len(statuses) > 0 {
			placeholders := sqlPlaceholders(len(statuses))
			whereClauses = append(whereClauses, `status IN (`+placeholders+`)`)
			for _, status := range statuses {
				args = append(args, status)
			}
		}

		if hasReading {
			whereClauses = append(whereClauses, `(status <> 'reading' OR latest_known_chapter IS NULL OR last_read_chapter IS NULL OR last_read_chapter < latest_known_chapter)`)
		}
	}

	if len(options.SourceIDs) > 0 {
		seenSourceIDs := make(map[int64]struct{}, len(options.SourceIDs))
		filteredSourceIDs := make([]int64, 0, len(options.SourceIDs))
		for _, sourceID := range options.SourceIDs {
			if sourceID <= 0 {
				continue
			}
			if _, exists := seenSourceIDs[sourceID]; exists {
				continue
			}
			seenSourceIDs[sourceID] = struct{}{}
			filteredSourceIDs = append(filteredSourceIDs, sourceID)
		}

		if len(filteredSourceIDs) > 0 {
			placeholders := sqlPlaceholders(len(filteredSourceIDs))

			whereClauses = append(whereClauses, `(trackers.source_id IN (`+placeholders+`) OR EXISTS (
				SELECT 1
				FROM tracker_sources ts
				WHERE ts.tracker_id = trackers.id
				  AND ts.source_id IN (`+placeholders+`)
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
		seenTagNames := make(map[string]struct{}, len(options.TagNames))
		for _, tagName := range options.TagNames {
			normalized := strings.TrimSpace(strings.ToLower(tagName))
			if normalized == "" {
				continue
			}
			if _, exists := seenTagNames[normalized]; exists {
				continue
			}
			seenTagNames[normalized] = struct{}{}

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

func (r *TrackerRepository) ListForPolling() ([]PollingTracker, error) {
	query := `
		SELECT
			t.id, t.title, t.status, t.source_id, t.source_item_id, t.source_url,
			t.latest_known_chapter, t.last_read_chapter, t.latest_release_at, s.key, t.last_checked_at,
			t.pending_lower_chapter, t.pending_lower_first_seen_at, t.latest_chapter_source_id
		FROM trackers t
		INNER JOIN sources s ON s.id = t.source_id
	`

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list trackers for polling: %w", err)
	}
	defer rows.Close()

	items := make([]PollingTracker, 0)
	for rows.Next() {
		var item PollingTracker
		var sourceItemID sql.NullString
		var latest sql.NullFloat64
		var lastRead sql.NullFloat64
		var latestReleaseAt sql.NullTime
		var lastCheckedAt sql.NullTime
		var pendingLower sql.NullFloat64
		var pendingLowerSeenAt sql.NullTime
		var latestChapterSourceID sql.NullInt64
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Status, &item.SourceID, &sourceItemID, &item.SourceURL,
			&latest, &lastRead, &latestReleaseAt, &item.SourceKey, &lastCheckedAt,
			&pendingLower, &pendingLowerSeenAt, &latestChapterSourceID,
		); err != nil {
			return nil, fmt.Errorf("scan polling tracker: %w", err)
		}
		if sourceItemID.Valid {
			item.SourceItemID = &sourceItemID.String
		}
		if latest.Valid {
			item.LatestKnownChapter = &latest.Float64
		}
		if lastRead.Valid {
			item.LastReadChapter = &lastRead.Float64
		}
		if latestReleaseAt.Valid {
			releaseAt := latestReleaseAt.Time.UTC()
			item.LatestReleaseAt = &releaseAt
		}
		if lastCheckedAt.Valid {
			checkedAt := lastCheckedAt.Time.UTC()
			item.LastCheckedAt = &checkedAt
		}
		if pendingLower.Valid {
			item.PendingLowerChapter = &pendingLower.Float64
		}
		if pendingLowerSeenAt.Valid {
			seenAt := pendingLowerSeenAt.Time.UTC()
			item.PendingLowerFirstSeenAt = &seenAt
		}
		if latestChapterSourceID.Valid {
			item.LatestChapterSourceID = &latestChapterSourceID.Int64
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate polling trackers: %w", err)
	}

	alternates, err := r.listPollingAlternateSources()
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].AlternateSources = alternates[items[index].ID]
	}

	return items, nil
}

// alternateSourcesQuery selects every tracker's non-primary linked sources.
// UpdatePollingState mirrors the primary source into tracker_sources, so that
// mirror row is filtered out to leave only genuine alternatives.
const alternateSourcesQuery = `
	SELECT ts.tracker_id, ts.source_id, s.key, ts.source_item_id, ts.source_url
	FROM tracker_sources ts
	INNER JOIN trackers t ON t.id = ts.tracker_id
	INNER JOIN sources s ON s.id = ts.source_id
	WHERE (ts.source_id <> t.source_id
	   OR LOWER(TRIM(ts.source_url)) <> LOWER(TRIM(t.source_url)))
`

const alternateSourcesOrder = ` ORDER BY ts.tracker_id ASC, s.name ASC, ts.id ASC`

// listPollingAlternateSources loads alternates for every tracker in the database,
// keyed by tracker id. Loaded in one query rather than per tracker to keep polling
// a two-query operation regardless of library size.
func (r *TrackerRepository) listPollingAlternateSources() (map[int64][]TrackerSourceRef, error) {
	rows, err := r.db.Query(alternateSourcesQuery + alternateSourcesOrder)
	if err != nil {
		return nil, fmt.Errorf("list polling alternate sources: %w", err)
	}
	return scanAlternateSources(rows)
}

// ListAlternateSourcesByTracker loads one profile's alternates, keyed by tracker
// id. The dashboard resolves covers and chapter links against a tracker's primary
// source; when that source is unreadable it falls back to these, so a blocked site
// does not leave the whole library without cover art or working chapter links.
func (r *TrackerRepository) ListAlternateSourcesByTracker(profileID int64) (map[int64][]TrackerSourceRef, error) {
	rows, err := r.db.Query(alternateSourcesQuery+` AND t.profile_id = ?`+alternateSourcesOrder, profileID)
	if err != nil {
		return nil, fmt.Errorf("list alternate sources by tracker: %w", err)
	}
	return scanAlternateSources(rows)
}

func scanAlternateSources(rows *sql.Rows) (map[int64][]TrackerSourceRef, error) {
	defer rows.Close()

	alternates := map[int64][]TrackerSourceRef{}
	for rows.Next() {
		var trackerID int64
		var source TrackerSourceRef
		var sourceItemID sql.NullString
		if err := rows.Scan(&trackerID, &source.SourceID, &source.SourceKey, &sourceItemID, &source.SourceURL); err != nil {
			return nil, fmt.Errorf("scan alternate source: %w", err)
		}
		if sourceItemID.Valid {
			source.SourceItemID = &sourceItemID.String
		}
		alternates[trackerID] = append(alternates[trackerID], source)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alternate sources: %w", err)
	}

	return alternates, nil
}

// MarkPollCheckedAt records that a poll cycle attempted this tracker even
// though no source answered. It touches nothing but last_checked_at — no
// chapter data, and not updated_at, since nothing about the tracker's content
// changed. Without this stamp a tracker whose sources are all dark can never
// become idle-skippable, and the poller retries it in full every cycle for as
// long as the outage lasts.
func (r *TrackerRepository) MarkPollCheckedAt(trackerID int64, checkedAt time.Time) error {
	if _, err := r.db.Exec(`UPDATE trackers SET last_checked_at = ? WHERE id = ?`, checkedAt.UTC(), trackerID); err != nil {
		return fmt.Errorf("mark poll checked: %w", err)
	}
	return nil
}

// UpdatePollingState persists one poll's outcome. It returns false when the
// write was skipped because the tracker no longer matches the snapshot the
// poll was computed from: a poll cycle can run for tens of minutes, and a user
// who repoints the tracker's source mid-cycle must not have that edit
// reverted by a write derived from the old source — worse, an unconditional
// write could leave source_id (new) mismatched with source_url (old), a state
// host validation rejects forever. The guard is the snapshot's source_id.
//
// The trackers UPDATE, the stale-mirror DELETE, and the mirror upsert run in
// one transaction: they describe a single poll outcome, and a crash between
// the DELETE and the INSERT used to drop the primary-source mirror row
// permanently.
func (r *TrackerRepository) UpdatePollingState(update PollingUpdate) (bool, error) {
	var latestReleaseValue any
	if update.LatestReleaseAt != nil {
		latestReleaseValue = update.LatestReleaseAt.UTC()
	}
	trimmedSourceURL := strings.TrimSpace(update.SourceURL)
	trimmedCurrentSourceURL := strings.TrimSpace(update.CurrentSourceURL)
	var sourceURLValue any
	if trimmedSourceURL != "" {
		sourceURLValue = trimmedSourceURL
	}
	var sourceItemIDValue any
	if update.SourceItemID != nil {
		trimmedSourceItemID := strings.TrimSpace(*update.SourceItemID)
		if trimmedSourceItemID != "" {
			sourceItemIDValue = trimmedSourceItemID
		}
	}

	checkedAt := update.CheckedAt.UTC()
	latestKnownChapter := update.LatestKnownChapter
	pendingLower := update.PendingLowerChapter
	var latestChapterSourceValue any
	if update.LatestChapterSourceID != nil {
		latestChapterSourceValue = *update.LatestChapterSourceID
	}

	tx, err := r.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin polling state tx: %w", err)
	}
	// With MaxOpenConns(1) the tx holds the pool's only connection, so every
	// statement below must go through tx, never r.db — a plain r.db call would
	// wait for the connection this tx holds and deadlock.
	defer tx.Rollback()

	// Every expression here is evaluated against the row as it stands before the
	// update, which is what lets latest_chapter_seen_at compare the incoming
	// chapter number against the stored one without the caller passing a flag —
	// the same way last_read_at is stamped in Update. pending_lower_first_seen_at
	// uses the same trick: it only restarts when the pending number itself
	// changes, so a value that keeps being reported keeps its original timestamp
	// and can age into confirmation.
	result, err := tx.Exec(`
		UPDATE trackers
		SET source_item_id = COALESCE(?, source_item_id),
			source_url = COALESCE(?, source_url),
			latest_chapter_seen_at = CASE
				WHEN ? IS NULL THEN latest_chapter_seen_at
				WHEN latest_known_chapter IS NOT ? THEN ?
				ELSE COALESCE(latest_chapter_seen_at, ?)
			END,
			latest_known_chapter = ?,
			latest_chapter_source_id = COALESCE(?, latest_chapter_source_id),
			pending_lower_first_seen_at = CASE
				WHEN ? IS NULL THEN NULL
				WHEN pending_lower_chapter IS NOT ? THEN ?
				ELSE COALESCE(pending_lower_first_seen_at, ?)
			END,
			pending_lower_chapter = ?,
			latest_release_at = CASE
				WHEN ? THEN NULL
				WHEN ? IS NOT NULL THEN ?
				ELSE latest_release_at
			END,
			last_checked_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND source_id = ?
	`, sourceItemIDValue, sourceURLValue,
		latestKnownChapter, latestKnownChapter, checkedAt, checkedAt,
		latestKnownChapter,
		latestChapterSourceValue,
		pendingLower, pendingLower, checkedAt, checkedAt,
		pendingLower,
		update.ClearLatestReleaseAt, latestReleaseValue, latestReleaseValue, checkedAt,
		update.TrackerID, update.SnapshotSourceID)
	if err != nil {
		return false, fmt.Errorf("update polling state: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("polling state rows affected: %w", err)
	}
	if affected == 0 {
		// The tracker was repointed (or deleted) after the poll snapshotted it.
		// Nothing was written, and the mirror statements below are derived from
		// the same stale snapshot, so they must be skipped too.
		return false, nil
	}

	if update.SourceID > 0 && trimmedSourceURL != "" {
		if trimmedCurrentSourceURL != "" && !strings.EqualFold(trimmedCurrentSourceURL, trimmedSourceURL) {
			if _, err := tx.Exec(`
				DELETE FROM tracker_sources
				WHERE tracker_id = ? AND source_id = ? AND LOWER(source_url) = LOWER(?)
			`, update.TrackerID, update.SourceID, trimmedCurrentSourceURL); err != nil {
				return false, fmt.Errorf("delete stale polling tracker source: %w", err)
			}
		}

		if _, err := tx.Exec(`
			INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(tracker_id, source_id, source_url)
			DO UPDATE SET
				source_item_id = excluded.source_item_id,
				updated_at = CURRENT_TIMESTAMP
		`, update.TrackerID, update.SourceID, update.SourceItemID, trimmedSourceURL); err != nil {
			return false, fmt.Errorf("upsert polling tracker source: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit polling state tx: %w", err)
	}
	return true, nil
}
