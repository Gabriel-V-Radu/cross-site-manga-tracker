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

// MetadataLink is one site a tracker is linked to, with that site's stored
// metadata for the series when there is any (Payload "" otherwise).
type MetadataLink struct {
	TrackerID  int64
	SourceID   int64
	SourceKey  string
	SourceName string
	SourceURL  string
	Payload    string
	FetchedAt  *time.Time
}

// ListMetadataLinks returns every tracker's linked sites — the primary and
// every tracker_sources row, the primary's mirror folded into it — with their
// stored metadata. One query for the whole library: the consolidation pass
// compares every tracker's inputs with what its stored view was built from.
func (r *MetadataRepository) ListMetadataLinks(ctx context.Context) ([]MetadataLink, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT links.tracker_id, links.source_id, s.key, s.name, links.source_url, sm.payload, sm.fetched_at
		FROM (
			SELECT id AS tracker_id, source_id, source_url FROM trackers
			UNION
			SELECT tracker_id, source_id, source_url FROM tracker_sources
		) links
		INNER JOIN sources s ON s.id = links.source_id
		LEFT JOIN source_metadata sm
			ON sm.source_id = links.source_id AND sm.source_url = LOWER(TRIM(links.source_url))
		ORDER BY links.tracker_id, links.source_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list metadata links: %w", err)
	}
	defer rows.Close()

	links := make([]MetadataLink, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var link MetadataLink
		var payload sql.NullString
		var fetchedAt sql.NullTime
		if err := rows.Scan(&link.TrackerID, &link.SourceID, &link.SourceKey, &link.SourceName, &link.SourceURL, &payload, &fetchedAt); err != nil {
			return nil, fmt.Errorf("scan metadata link: %w", err)
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
		return nil, fmt.Errorf("iterate metadata links: %w", err)
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
