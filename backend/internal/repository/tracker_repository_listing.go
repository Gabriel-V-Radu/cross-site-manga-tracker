package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

func (r *TrackerRepository) List(ctx context.Context, options TrackerListOptions) ([]models.Tracker, error) {
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
		//
		// When a poll found the chapter well after it came out — a finished
		// series polled every few days that got an extra, a site that indexed
		// it late — the find time ranks it instead, so the reader sees it on
		// top rather than days down the list. latest_chapter_found_at is only
		// ever set by a poll that moved the number up, so trackers whose number
		// came from creation or an edit keep ranking by release date exactly as
		// before. SQLite's two-argument MAX is NULL if either side is, hence
		// the fallback inside the second COALESCE.
		// How many chapters are left to read, most first. Trackers with
		// nothing pending — caught up, never started, or with no chapter
		// known — rank NULL, which a descending sort puts last.
		"unread": `CASE
			WHEN latest_known_chapter IS NULL OR last_read_chapter IS NULL OR latest_known_chapter <= last_read_chapter THEN NULL
			ELSE latest_known_chapter - last_read_chapter
		END`,
		"latest_known_chapter": `CASE WHEN latest_known_chapter IS NULL THEN NULL ELSE MAX(
			COALESCE(latest_release_at, latest_chapter_seen_at, created_at),
			COALESCE(latest_chapter_found_at, latest_release_at, latest_chapter_seen_at, created_at)
		) END`,
	}
	sortField, ok := validSortFields[options.SortBy]
	if !ok {
		sortField = validSortFields["latest_known_chapter"]
	}

	order := strings.ToUpper(options.Order)
	if order != "ASC" && order != "DESC" {
		order = "DESC"
	}

	query := `SELECT ` + trackerColumns + ` FROM trackers`

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

	trackers, err := r.queryTrackers(ctx, query, args)
	if err != nil {
		return nil, err
	}

	if len(trackers) == 0 {
		return trackers, nil
	}

	tagsByTracker, err := r.ListTagsByTrackerIDs(ctx, options.ProfileID, trackerIDs(trackers))
	if err != nil {
		return nil, fmt.Errorf("list tracker tags: %w", err)
	}
	for index := range trackers {
		trackers[index].Tags = tagsByTracker[trackers[index].ID]
	}

	return trackers, nil
}

// queryTrackers drains the cursor into a slice and closes it before returning.
// The tag lookup that follows in List is a second query on the same
// single-connection pool, so it must not start while this cursor still holds
// the connection.
func (r *TrackerRepository) queryTrackers(ctx context.Context, query string, args []any) ([]models.Tracker, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
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

func (r *TrackerRepository) Count(ctx context.Context, options TrackerListOptions) (int, error) {
	query := `SELECT COUNT(1) FROM trackers`
	whereClauses, args := buildTrackerListFilters(options)
	if len(whereClauses) > 0 {
		query += ` WHERE ` + strings.Join(whereClauses, " AND ")
	}

	var total int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
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
		// The query is lowered in Go with Unicode rules, so the columns go
		// through unicode_lower (registered in the database package) rather
		// than SQLite's ASCII-only LOWER(): with LOWER() a title starting with
		// a non-ASCII capital ('Ōkami') stayed as it was and 'ōkami' could
		// never find it.
		normalizedQuery := searchutil.Normalize(options.Query)
		if normalizedQuery != "" {
			queryTokens := searchutil.TokenizeNormalized(normalizedQuery)
			if len(queryTokens) == 0 {
				whereClauses = append(whereClauses, `(unicode_lower(trackers.title) LIKE ? OR unicode_lower(COALESCE(trackers.related_titles, '')) LIKE ?)`)
				queryLike := "%" + normalizedQuery + "%"
				args = append(args, queryLike, queryLike)
			} else {
				for _, token := range queryTokens {
					tokenLike := "%" + token + "%"
					whereClauses = append(whereClauses, `(unicode_lower(trackers.title) LIKE ? OR unicode_lower(COALESCE(trackers.related_titles, '')) LIKE ?)`)
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

		// Reading on its own is the reading queue, so it leaves out what has
		// been read up to the latest chapter. With any other filter on, the
		// reader is looking for a particular title among the ones they read,
		// and a title they are caught up on is as much one of those as any.
		if hasReading && !options.Narrowed() {
			whereClauses = append(whereClauses, `(status <> 'reading' OR latest_known_chapter IS NULL OR last_read_chapter IS NULL OR last_read_chapter < latest_known_chapter)`)
		}
	}

	// Type and publication status: any of the values asked for. Genres: all
	// of them, each its own clause — someone asking for Action and Romance
	// wants the titles that are both.
	if seriesTypes := distinctTrimmed(options.SeriesTypes); len(seriesTypes) > 0 {
		whereClauses = append(whereClauses, `EXISTS (
			SELECT 1 FROM tracker_metadata tm
			WHERE tm.tracker_id = trackers.id AND tm.series_type IN (`+sqlPlaceholders(len(seriesTypes))+`)
		)`)
		for _, value := range seriesTypes {
			args = append(args, value)
		}
	}
	if seriesStatuses := distinctTrimmed(options.SeriesStatuses); len(seriesStatuses) > 0 {
		whereClauses = append(whereClauses, `EXISTS (
			SELECT 1 FROM tracker_metadata tm
			WHERE tm.tracker_id = trackers.id AND tm.series_status IN (`+sqlPlaceholders(len(seriesStatuses))+`)
		)`)
		for _, value := range seriesStatuses {
			args = append(args, value)
		}
	}
	for _, genre := range distinctTrimmed(options.Genres) {
		whereClauses = append(whereClauses, `EXISTS (
			SELECT 1 FROM tracker_metadata tm, json_each(tm.genres) genre
			WHERE tm.tracker_id = trackers.id AND genre.value = ?
		)`)
		args = append(args, genre)
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
			name := strings.TrimSpace(tagName)
			normalized := strings.ToLower(name)
			if normalized == "" {
				continue
			}
			if _, exists := seenTagNames[normalized]; exists {
				continue
			}
			seenTagNames[normalized] = struct{}{}

			// The name is compared as stored, not through LOWER(): SQLite's
			// lower() folds ASCII only, so 'Época' stayed 'Época' in SQL while
			// Go had lowered the filter to 'época', and a tag starting with an
			// accented capital never matched a single tracker. The column's
			// NOCASE collation still makes the ASCII case irrelevant, which is
			// exactly the rule the tag names are kept unique by.
			whereClauses = append(whereClauses, `EXISTS (
				SELECT 1
				FROM tracker_tags tt
				INNER JOIN custom_tags ct ON ct.id = tt.tag_id
				WHERE tt.tracker_id = trackers.id
				  AND ct.profile_id = ?
				  AND ct.name = ?
			)`)
			args = append(args, options.ProfileID, name)
		}
	}

	return whereClauses, args
}

func (r *TrackerRepository) ListForPolling(ctx context.Context) ([]PollingTracker, error) {
	items, err := r.listPollingTrackers(ctx)
	if err != nil {
		return nil, err
	}

	alternates, err := r.listPollingAlternateSources(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].AlternateSources = alternates[items[index].ID]
	}

	return items, nil
}

// pollingTrackersQuery is listPollingTrackers' statement, a constant so the
// test that pins its query plan runs EXPLAIN on the very text the poller runs.
//
// The unary plus on m.source_id is deliberate: it keeps that term out of index
// selection without changing what it matches. Left bare, the planner answered
// the subquery through idx_tracker_sources_source_id — an index that happens
// to return a site's links in id order, sparing it the ORDER BY's sort — and
// so walked every link of the tracker's site, hundreds of rows, per tracker.
// With the term disqualified the only index left for the subquery is the
// UNIQUE(tracker_id, source_id, source_url) one, entered by tracker_id: the
// tracker's own one to five links, filtered and sorted in passing. The
// database has no sqlite_stat tables and nothing runs ANALYZE, so the choice
// cannot be left to statistics; INDEXED BY would pin it too, but by an
// auto-generated index name that turns every poll into an error the day the
// table is rebuilt, where the plus merely stops helping.
const pollingTrackersQuery = `
	SELECT
		t.id, t.profile_id, t.title, t.status, t.source_id, t.source_item_id, t.source_url,
		t.latest_known_chapter, t.last_read_chapter, t.latest_release_at, s.key, t.last_checked_at,
		t.pending_lower_chapter, t.pending_lower_first_seen_at, t.latest_chapter_source_id,
		mirror.last_polled_at, t.latest_chapter_seen_at, t.latest_chapter_found_at,
		COALESCE(tm.series_status, '')
	FROM trackers t
	INNER JOIN sources s ON s.id = t.source_id
	LEFT JOIN tracker_metadata tm ON tm.tracker_id = t.id
	LEFT JOIN tracker_sources mirror ON mirror.id = (
		SELECT m.id FROM tracker_sources m
		WHERE m.tracker_id = t.id AND +m.source_id = t.source_id
		  AND LOWER(TRIM(m.source_url)) = LOWER(TRIM(t.source_url))
		ORDER BY m.id
		LIMIT 1
	)
`

// listPollingTrackers drains its cursor before returning so the alternates
// query that follows is not issued against a connection this one still holds.
//
// The primary's poll state lives on its mirror row in tracker_sources. The
// join goes through the row's id rather than aggregating the timestamp
// because an aggregate loses the column's DATETIME type, and the driver then
// hands back a string that will not scan into a time.
func (r *TrackerRepository) listPollingTrackers(ctx context.Context) ([]PollingTracker, error) {
	rows, err := r.db.QueryContext(ctx, pollingTrackersQuery)
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
		var primaryPolledAt sql.NullTime
		var seenAt sql.NullTime
		var foundAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.ProfileID, &item.Title, &item.Status, &item.SourceID, &sourceItemID, &item.SourceURL,
			&latest, &lastRead, &latestReleaseAt, &item.SourceKey, &lastCheckedAt,
			&pendingLower, &pendingLowerSeenAt, &latestChapterSourceID,
			&primaryPolledAt, &seenAt, &foundAt, &item.SeriesStatus,
		); err != nil {
			return nil, fmt.Errorf("scan polling tracker: %w", err)
		}
		if seenAt.Valid {
			value := seenAt.Time.UTC()
			item.LatestChapterSeenAt = &value
		}
		if foundAt.Valid {
			value := foundAt.Time.UTC()
			item.LatestChapterFoundAt = &value
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
		if primaryPolledAt.Valid {
			polledAt := primaryPolledAt.Time.UTC()
			item.PrimaryLastPolledAt = &polledAt
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate polling trackers: %w", err)
	}

	return items, nil
}

// alternateSourcesQuery selects every tracker's non-primary linked sources.
// UpdatePollingState mirrors the primary source into tracker_sources, so that
// mirror row is filtered out to leave only genuine alternatives.
const alternateSourcesQuery = `
	SELECT ts.tracker_id, ts.source_id, s.key, ts.source_item_id, ts.source_url,
		ts.last_polled_at, ts.in_step
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
func (r *TrackerRepository) listPollingAlternateSources(ctx context.Context) (map[int64][]TrackerSourceRef, error) {
	rows, err := r.db.QueryContext(ctx, alternateSourcesQuery+alternateSourcesOrder)
	if err != nil {
		return nil, fmt.Errorf("list polling alternate sources: %w", err)
	}
	return scanAlternateSources(rows)
}

// ListAlternateSourcesByTracker loads one profile's alternates, keyed by tracker
// id. The dashboard resolves covers and chapter links against a tracker's primary
// source; when that source is unreadable it falls back to these, so a blocked site
// does not leave the whole library without cover art or working chapter links.
func (r *TrackerRepository) ListAlternateSourcesByTracker(ctx context.Context, profileID int64) (map[int64][]TrackerSourceRef, error) {
	rows, err := r.db.QueryContext(ctx, alternateSourcesQuery+` AND t.profile_id = ?`+alternateSourcesOrder, profileID)
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
		var lastPolledAt sql.NullTime
		if err := rows.Scan(&trackerID, &source.SourceID, &source.SourceKey, &sourceItemID, &source.SourceURL,
			&lastPolledAt, &source.InStep); err != nil {
			return nil, fmt.Errorf("scan alternate source: %w", err)
		}
		if sourceItemID.Valid {
			source.SourceItemID = &sourceItemID.String
		}
		if lastPolledAt.Valid {
			polledAt := lastPolledAt.Time.UTC()
			source.LastPolledAt = &polledAt
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
// long as the outage lasts. The sites it asked are stamped too, so the next
// cycle's rotation starts with a site that was not just tried.
func (r *TrackerRepository) MarkPollCheckedAt(ctx context.Context, trackerID int64, checkedAt time.Time, polls []SourcePoll) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark poll checked tx: %w", err)
	}
	// Every statement goes through tx: see UpdatePollingState.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE trackers SET last_checked_at = ? WHERE id = ?`, checkedAt.UTC(), trackerID); err != nil {
		return fmt.Errorf("mark poll checked: %w", err)
	}
	if err := recordSourcePolls(ctx, tx, trackerID, checkedAt, polls); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark poll checked tx: %w", err)
	}
	return nil
}

// recordSourcePolls writes what one poll learned about each site it asked.
// It only ever updates existing links: a link the user removed mid-cycle must
// stay removed, and the primary's mirror row is written before this runs.
func recordSourcePolls(ctx context.Context, tx *sql.Tx, trackerID int64, polledAt time.Time, polls []SourcePoll) error {
	for _, poll := range polls {
		sourceID, sourceURL := poll.SourceID, poll.SourceURL
		if poll.Primary {
			err := tx.QueryRowContext(ctx, `SELECT source_id, source_url FROM trackers WHERE id = ?`, trackerID).Scan(&sourceID, &sourceURL)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read primary for source poll: %w", err)
			}
		}
		if sourceID <= 0 || strings.TrimSpace(sourceURL) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tracker_sources
			SET last_polled_at = ?,
				in_step = CASE WHEN ? THEN ? ELSE in_step END
			WHERE tracker_id = ? AND source_id = ? AND LOWER(TRIM(source_url)) = LOWER(TRIM(?))
		`, polledAt.UTC(), poll.Answered, poll.InStep, trackerID, sourceID, sourceURL); err != nil {
			return fmt.Errorf("record source poll: %w", err)
		}
	}
	return nil
}

// UpdatePollingState persists one poll's outcome. It returns false when the
// write was skipped because the tracker no longer matches the snapshot the
// poll was computed from: a poll cycle can run for tens of minutes, and a user
// who repoints the tracker's source mid-cycle must not have that edit
// reverted by a write derived from the old source — worse, an unconditional
// write could leave source_id (new) mismatched with source_url (old), a state
// host validation rejects forever. The guard is the snapshot's source_id,
// source_url and latest_known_chapter: a repoint to another series on the
// same site, or a chapter repaired by hand, is as much an edit the stale
// write would revert as a change of site. The chapter is compared with IS so
// a snapshot with no chapter still matches a row with none, and a REAL read
// back into a float64 binds to the identical value, so equality is exact.
//
// The trackers UPDATE, the stale-mirror DELETE, and the mirror upsert run in
// one transaction: they describe a single poll outcome, and a crash between
// the DELETE and the INSERT used to drop the primary-source mirror row
// permanently.
func (r *TrackerRepository) UpdatePollingState(ctx context.Context, update PollingUpdate) (bool, error) {
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

	tx, err := r.db.BeginTx(ctx, nil)
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
	result, err := tx.ExecContext(ctx, `
		UPDATE trackers
		SET source_item_id = COALESCE(?, source_item_id),
			source_url = COALESCE(?, source_url),
			latest_chapter_seen_at = CASE
				WHEN ? IS NULL THEN latest_chapter_seen_at
				WHEN latest_known_chapter IS NOT ? THEN ?
				ELSE COALESCE(latest_chapter_seen_at, ?)
			END,
			-- Found: a poll moved the number up. A first fill is not a
			-- find (the tracker knew no number to move from), and a
			-- correction down leaves no find standing.
			latest_chapter_found_at = CASE
				WHEN ? IS NULL OR latest_known_chapter IS NULL THEN latest_chapter_found_at
				WHEN ? > latest_known_chapter THEN ?
				WHEN ? < latest_known_chapter THEN NULL
				ELSE latest_chapter_found_at
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
		WHERE id = ? AND source_id = ? AND source_url = ? AND latest_known_chapter IS ?
	`, sourceItemIDValue, sourceURLValue,
		latestKnownChapter, latestKnownChapter, checkedAt, checkedAt,
		latestKnownChapter, latestKnownChapter, checkedAt, latestKnownChapter,
		latestKnownChapter,
		latestChapterSourceValue,
		pendingLower, pendingLower, checkedAt, checkedAt,
		pendingLower,
		update.ClearLatestReleaseAt, latestReleaseValue, latestReleaseValue, checkedAt,
		update.TrackerID, update.SnapshotSourceID, update.SnapshotSourceURL, update.SnapshotLatestKnownChapter)
	if err != nil {
		return false, fmt.Errorf("update polling state: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("polling state rows affected: %w", err)
	}
	if affected == 0 {
		// The tracker was repointed, had its chapter repaired, or was deleted
		// after the poll snapshotted it.
		// Nothing was written, and the mirror statements below are derived from
		// the same stale snapshot, so they must be skipped too.
		return false, nil
	}

	if update.SourceID > 0 && trimmedSourceURL != "" {
		if trimmedCurrentSourceURL != "" && !strings.EqualFold(trimmedCurrentSourceURL, trimmedSourceURL) {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM tracker_sources
				WHERE tracker_id = ? AND source_id = ? AND LOWER(source_url) = LOWER(?)
			`, update.TrackerID, update.SourceID, trimmedCurrentSourceURL); err != nil {
				return false, fmt.Errorf("delete stale polling tracker source: %w", err)
			}
		}

		if _, err := tx.ExecContext(ctx, `
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

	if err := recordSourcePolls(ctx, tx, update.TrackerID, checkedAt, update.SourcePolls); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit polling state tx: %w", err)
	}
	return true, nil
}
