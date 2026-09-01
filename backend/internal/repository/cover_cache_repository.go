package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CoverCacheRepository persists the dashboard's cover cache. The in-memory
// map stays the hot path; this store only has to survive restarts, so writes
// are write-through and the whole fresh set is loaded once at construction.
type CoverCacheRepository struct {
	db *sql.DB
}

func NewCoverCacheRepository(db *sql.DB) *CoverCacheRepository {
	return &CoverCacheRepository{db: db}
}

type CoverCacheRow struct {
	CacheKey  string
	CoverURL  string
	SourceKey string
	Found     bool
	ExpiresAt time.Time
	// LocalPath names the downloaded copy of the cover under the handler's
	// cover directory. Non-empty entries are permanent: they serve the image
	// from this host and are exempt from the expiry sweep.
	LocalPath string
}

// LoadFresh sweeps expired remote-only rows and returns the rest. Called once
// at startup to seed the in-memory cache, which is what stops a restart from
// turning the next dashboard render into a resolve storm. Entries with a
// local copy never expire — the file on disk is the cache.
func (r *CoverCacheRepository) LoadFresh(ctx context.Context) ([]CoverCacheRow, error) {
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, `DELETE FROM cover_cache WHERE expires_at <= ? AND local_path = ''`, now); err != nil {
		return nil, fmt.Errorf("sweep expired covers: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT cache_key, cover_url, source_key, found, expires_at, local_path
		FROM cover_cache
		WHERE expires_at > ? OR local_path <> ''
	`, now)
	if err != nil {
		return nil, fmt.Errorf("load cover cache: %w", err)
	}
	defer rows.Close()

	entries := make([]CoverCacheRow, 0)
	for rows.Next() {
		var entry CoverCacheRow
		if err := rows.Scan(&entry.CacheKey, &entry.CoverURL, &entry.SourceKey, &entry.Found, &entry.ExpiresAt, &entry.LocalPath); err != nil {
			return nil, fmt.Errorf("scan cover cache row: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cover cache rows: %w", err)
	}
	return entries, nil
}

func (r *CoverCacheRepository) Upsert(ctx context.Context, entry CoverCacheRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO cover_cache (cache_key, cover_url, source_key, found, expires_at, local_path, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(cache_key) DO UPDATE SET
			cover_url = excluded.cover_url,
			source_key = excluded.source_key,
			found = excluded.found,
			expires_at = excluded.expires_at,
			local_path = excluded.local_path,
			updated_at = CURRENT_TIMESTAMP
	`, entry.CacheKey, entry.CoverURL, entry.SourceKey, entry.Found, entry.ExpiresAt.UTC(), entry.LocalPath)
	if err != nil {
		return fmt.Errorf("upsert cover cache entry: %w", err)
	}
	return nil
}

func (r *CoverCacheRepository) Delete(ctx context.Context, cacheKey string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM cover_cache WHERE cache_key = ?`, cacheKey); err != nil {
		return fmt.Errorf("delete cover cache entry: %w", err)
	}
	return nil
}

// DeleteNegatives drops every "no cover found" entry, mirroring the in-memory
// invalidation that runs when a tracker's linked sources change.
func (r *CoverCacheRepository) DeleteNegatives(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM cover_cache WHERE found = 0`); err != nil {
		return fmt.Errorf("delete negative cover entries: %w", err)
	}
	return nil
}
