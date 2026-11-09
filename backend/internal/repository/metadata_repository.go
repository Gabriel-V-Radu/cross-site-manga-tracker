package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MetadataRepository stores what sites say about series (source_metadata) and
// each tracker's consolidated view of it (tracker_metadata). See migration
// 0002 for the tables and internal/metadata for the policy.
type MetadataRepository struct {
	db *sql.DB
}

func NewMetadataRepository(db *sql.DB) *MetadataRepository {
	return &MetadataRepository{db: db}
}

// metadataURLKey is the normalisation source_metadata.source_url is stored
// under, the same LOWER(TRIM()) tracker_sources rows are matched with.
func metadataURLKey(rawURL string) string {
	return strings.ToLower(strings.TrimSpace(rawURL))
}

// SourceMetadataFetchedAt returns when the site's metadata for the series was
// last stored, nil when it never was.
func (r *MetadataRepository) SourceMetadataFetchedAt(ctx context.Context, sourceID int64, sourceURL string) (*time.Time, error) {
	var fetchedAt time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT fetched_at FROM source_metadata WHERE source_id = ? AND source_url = ?
	`, sourceID, metadataURLKey(sourceURL)).Scan(&fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source metadata age: %w", err)
	}
	value := fetchedAt.UTC()
	return &value, nil
}

// UpsertSourceMetadata stores one site's metadata (JSON) for one series.
func (r *MetadataRepository) UpsertSourceMetadata(ctx context.Context, sourceID int64, sourceURL string, payload string, fetchedAt time.Time) error {
	key := metadataURLKey(sourceURL)
	if sourceID <= 0 || key == "" {
		return fmt.Errorf("upsert source metadata: missing source or url")
	}
	// A stored row is read everywhere as "this site has metadata for the
	// series" (ListMetadataBackfillLinks does not even look inside it), so a
	// row with nothing in it must never be written.
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("upsert source metadata: empty payload")
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO source_metadata (source_id, source_url, payload, fetched_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id, source_url) DO UPDATE SET
			payload = excluded.payload,
			fetched_at = excluded.fetched_at
	`, sourceID, key, payload, fetchedAt.UTC()); err != nil {
		return fmt.Errorf("upsert source metadata: %w", err)
	}
	return nil
}

// MetadataLink is one site a tracker is linked to. FetchedAt is when that
// site's metadata for the series was stored, nil when it has none. Payload is
// the stored metadata itself and is only filled by ListMetadataLinks: the
// payloads are nearly all of the table's weight, and the passes that run
// every few minutes need to know that a row exists and how old it is, not
// what it says.
type MetadataLink struct {
	TrackerID  int64
	SourceID   int64
	SourceKey  string
	SourceName string
	SourceURL  string
	Payload    string
	FetchedAt  *time.Time
}

// The three link queries below share these pieces. metadataLinksFrom is every
// tracker's linked sites: the primary and every tracker_sources row, the
// primary's mirror folded into it (UNION folds the exact duplicates,
// queryMetadataLinks the ones that differ in case or whitespace).
// metadataLinksJoin attaches a site's stored row by the key source_metadata
// is stored under. The order ends on the URL so two links of one tracker to
// the same site come back the same way round from every query: which of them
// the backfill asks first, and the order a view's sources are merged in, must
// not depend on which query produced the rows.
const (
	metadataLinksFrom = `
		FROM (
			SELECT id AS tracker_id, source_id, source_url FROM trackers
			UNION
			SELECT tracker_id, source_id, source_url FROM tracker_sources
		) links
		INNER JOIN sources s ON s.id = links.source_id`
	metadataLinksJoin = `
		LEFT JOIN source_metadata sm
			ON sm.source_id = links.source_id AND sm.source_url = LOWER(TRIM(links.source_url))`
	metadataLinksOrder = `
		ORDER BY links.tracker_id, links.source_id, links.source_url`
)

// ListMetadataLinkStamps returns every tracker's linked sites with when each
// site's metadata was stored, and no payloads. It is what the consolidation
// pass compares against the signature every stored view was built from; on
// the deployed library the payloads it leaves out were about a megabyte per
// call, read every few minutes to conclude that nothing had changed.
func (r *MetadataRepository) ListMetadataLinkStamps(ctx context.Context) ([]MetadataLink, error) {
	return r.queryMetadataLinks(ctx, "list metadata link stamps", `
		SELECT links.tracker_id, links.source_id, s.key, s.name, links.source_url, NULL, sm.fetched_at`+
		metadataLinksFrom+metadataLinksJoin+metadataLinksOrder)
}

// maxMetadataLinkTrackers bounds one statement's IN list. A fresh database
// rebuilds every view in its first pass, and the list must stay under
// SQLite's bound-parameter limit however large the library is.
const maxMetadataLinkTrackers = 500

// ListMetadataLinks returns the given trackers' linked sites with their
// stored metadata, payloads included — what a view is built from, asked only
// for the trackers whose view has to be rebuilt.
func (r *MetadataRepository) ListMetadataLinks(ctx context.Context, trackerIDs []int64) ([]MetadataLink, error) {
	links := make([]MetadataLink, 0)
	for start := 0; start < len(trackerIDs); start += maxMetadataLinkTrackers {
		batch := trackerIDs[start:min(start+maxMetadataLinkTrackers, len(trackerIDs))]
		args := make([]any, 0, len(batch))
		for _, trackerID := range batch {
			args = append(args, trackerID)
		}
		// Each batch's cursor is drained and closed inside queryMetadataLinks
		// before the next statement is issued: the pool has one connection.
		batchLinks, err := r.queryMetadataLinks(ctx, "list metadata links", `
			SELECT links.tracker_id, links.source_id, s.key, s.name, links.source_url, sm.payload, sm.fetched_at`+
			metadataLinksFrom+metadataLinksJoin+`
			WHERE links.tracker_id IN (`+sqlPlaceholders(len(batch))+`)`+
			metadataLinksOrder, args...)
		if err != nil {
			return nil, err
		}
		links = append(links, batchLinks...)
	}
	return links, nil
}

// ListMetadataBackfillLinks returns the linked sites of the trackers that
// have no stored metadata from any of them — the only trackers the backfill
// has anything to do for, and after the first days there are none, so the
// pass that runs every minute costs one query that returns no rows. Whether
// a tracker has metadata is settled on source_metadata's primary key alone,
// without reading a row: UpsertSourceMetadata is the table's only writer and
// refuses an empty payload, so a row that exists is metadata.
func (r *MetadataRepository) ListMetadataBackfillLinks(ctx context.Context) ([]MetadataLink, error) {
	return r.queryMetadataLinks(ctx, "list metadata backfill links", metadataBackfillLinksQuery)
}

// metadataBackfillLinksQuery is a constant so the test that pins its plan —
// source_metadata reached through its primary-key index only — runs EXPLAIN
// on the statement the loop actually issues.
const metadataBackfillLinksQuery = `
		SELECT links.tracker_id, links.source_id, s.key, s.name, links.source_url, NULL, NULL` +
	metadataLinksFrom + `
		WHERE links.tracker_id NOT IN (
			SELECT t.id FROM trackers t
			WHERE EXISTS (
				SELECT 1 FROM source_metadata sm
				WHERE sm.source_id = t.source_id AND sm.source_url = LOWER(TRIM(t.source_url))
			)
			UNION ALL
			SELECT ts.tracker_id FROM tracker_sources ts
			WHERE EXISTS (
				SELECT 1 FROM source_metadata sm
				WHERE sm.source_id = ts.source_id AND sm.source_url = LOWER(TRIM(ts.source_url))
			)
		)` + metadataLinksOrder

// queryMetadataLinks runs one of the link queries and reads it to the end, so
// the connection is free again when it returns.
func (r *MetadataRepository) queryMetadataLinks(ctx context.Context, what string, query string, args ...any) ([]MetadataLink, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()

	links := make([]MetadataLink, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var link MetadataLink
		var payload sql.NullString
		var fetchedAt sql.NullTime
		if err := rows.Scan(&link.TrackerID, &link.SourceID, &link.SourceKey, &link.SourceName, &link.SourceURL, &payload, &fetchedAt); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		// UNION folds exact duplicates only; the primary's mirror row can
		// differ from the trackers row in case or whitespace.
		dedupeKey := fmt.Sprintf("%d|%d|%s", link.TrackerID, link.SourceID, metadataURLKey(link.SourceURL))
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}
		if payload.Valid {
			link.Payload = payload.String
		}
		if fetchedAt.Valid {
			value := fetchedAt.Time.UTC()
			link.FetchedAt = &value
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: iterate: %w", what, err)
	}
	return links, nil
}

// TrackerMetadataRow is one tracker's consolidated metadata as stored.
type TrackerMetadataRow struct {
	TrackerID    int64
	SeriesType   string
	SeriesStatus string
	// GenresJSON is a JSON array of canonical genre names.
	GenresJSON string
	// Payload is metadata.Consolidated as JSON.
	Payload   string
	Signature string
	UpdatedAt time.Time
}

// TrackerMetadataSignatures returns the signature each stored view was built
// from, by tracker id.
func (r *MetadataRepository) TrackerMetadataSignatures(ctx context.Context) (map[int64]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT tracker_id, signature FROM tracker_metadata`)
	if err != nil {
		return nil, fmt.Errorf("list tracker metadata signatures: %w", err)
	}
	defer rows.Close()
	signatures := map[int64]string{}
	for rows.Next() {
		var trackerID int64
		var signature string
		if err := rows.Scan(&trackerID, &signature); err != nil {
			return nil, fmt.Errorf("scan tracker metadata signature: %w", err)
		}
		signatures[trackerID] = signature
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker metadata signatures: %w", err)
	}
	return signatures, nil
}

// SaveTrackerMetadata writes several consolidated views in one transaction. A
// tracker deleted since the pass read it is skipped rather than failing the
// batch: the row would only be cascaded away again.
func (r *MetadataRepository) SaveTrackerMetadata(ctx context.Context, rowsToSave []TrackerMetadataRow) error {
	if len(rowsToSave) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tracker metadata tx: %w", err)
	}
	// With MaxOpenConns(1) every statement below must go through tx.
	defer tx.Rollback()
	for _, row := range rowsToSave {
		genres := row.GenresJSON
		if strings.TrimSpace(genres) == "" {
			genres = "[]"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tracker_metadata (tracker_id, series_type, series_status, genres, payload, signature, updated_at)
			SELECT id, ?, ?, ?, ?, ?, ? FROM trackers WHERE id = ?
			ON CONFLICT(tracker_id) DO UPDATE SET
				series_type = excluded.series_type,
				series_status = excluded.series_status,
				genres = excluded.genres,
				payload = excluded.payload,
				signature = excluded.signature,
				updated_at = excluded.updated_at
		`, row.SeriesType, row.SeriesStatus, genres, row.Payload, row.Signature, row.UpdatedAt.UTC(), row.TrackerID); err != nil {
			return fmt.Errorf("save tracker metadata %d: %w", row.TrackerID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tracker metadata tx: %w", err)
	}
	return nil
}

// GetTrackerMetadata returns one tracker's consolidated view, nil when none
// has been built (or the tracker is not the profile's).
func (r *MetadataRepository) GetTrackerMetadata(ctx context.Context, profileID int64, trackerID int64) (*TrackerMetadataRow, error) {
	var row TrackerMetadataRow
	err := r.db.QueryRowContext(ctx, `
		SELECT tm.tracker_id, tm.series_type, tm.series_status, tm.genres, tm.payload, tm.signature, tm.updated_at
		FROM tracker_metadata tm
		INNER JOIN trackers t ON t.id = tm.tracker_id
		WHERE tm.tracker_id = ? AND t.profile_id = ?
	`, trackerID, profileID).Scan(&row.TrackerID, &row.SeriesType, &row.SeriesStatus, &row.GenresJSON, &row.Payload, &row.Signature, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tracker metadata: %w", err)
	}
	return &row, nil
}

// MetadataFacets counts one profile's trackers by type, publication status
// and genre: the filter menus offer only what the library actually has, with
// how many titles each option would leave.
type MetadataFacets struct {
	Types    map[string]int
	Statuses map[string]int
	Genres   map[string]int
}

func (r *MetadataRepository) ListMetadataFacets(ctx context.Context, profileID int64) (MetadataFacets, error) {
	facets := MetadataFacets{Types: map[string]int{}, Statuses: map[string]int{}, Genres: map[string]int{}}

	count := func(query string, into map[string]int) error {
		rows, err := r.db.QueryContext(ctx, query, profileID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			var total int
			if err := rows.Scan(&value, &total); err != nil {
				return err
			}
			if strings.TrimSpace(value) != "" {
				into[value] = total
			}
		}
		return rows.Err()
	}

	if err := count(`
		SELECT tm.series_type, COUNT(1) FROM tracker_metadata tm
		INNER JOIN trackers t ON t.id = tm.tracker_id
		WHERE t.profile_id = ? GROUP BY tm.series_type
	`, facets.Types); err != nil {
		return facets, fmt.Errorf("count trackers by type: %w", err)
	}
	if err := count(`
		SELECT tm.series_status, COUNT(1) FROM tracker_metadata tm
		INNER JOIN trackers t ON t.id = tm.tracker_id
		WHERE t.profile_id = ? GROUP BY tm.series_status
	`, facets.Statuses); err != nil {
		return facets, fmt.Errorf("count trackers by publication status: %w", err)
	}
	if err := count(`
		SELECT genre.value, COUNT(1) FROM tracker_metadata tm
		INNER JOIN trackers t ON t.id = tm.tracker_id, json_each(tm.genres) genre
		WHERE t.profile_id = ? GROUP BY genre.value
	`, facets.Genres); err != nil {
		return facets, fmt.Errorf("count trackers by genre: %w", err)
	}
	return facets, nil
}
