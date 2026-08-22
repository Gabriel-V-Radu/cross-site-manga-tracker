package repository

import (
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
}

// LoadFresh sweeps expired rows and returns the rest. Called once at startup
// to seed the in-memory cache, which is what stops a restart from turning the
// next dashboard render into a resolve storm.
func (r *CoverCacheRepository) LoadFresh() ([]CoverCacheRow, error) {
	now := time.Now().UTC()
	if _, err := r.db.Exec(`DELETE FROM cover_cache WHERE expires_at <= ?`, now); err != nil {
		return nil, fmt.Errorf("sweep expired covers: %w", err)
	}

	rows, err := r.db.Query(`
		SELECT cache_key, cover_url, source_key, found, expires_at
		FROM cover_cache
		WHERE expires_at > ?
	`, now)
	if err != nil {
		return nil, fmt.Errorf("load cover cache: %w", err)
	}
	defer rows.Close()

	entries := make([]CoverCacheRow, 0)
	for rows.Next() {
		var entry CoverCacheRow
		if err := rows.Scan(&entry.CacheKey, &entry.CoverURL, &entry.SourceKey, &entry.Found, &entry.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan cover cache row: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cover cache rows: %w", err)
	}
	return entries, nil
}

func (r *CoverCacheRepository) Upsert(entry CoverCacheRow) error {
	_, err := r.db.Exec(`
		INSERT INTO cover_cache (cache_key, cover_url, source_key, found, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(cache_key) DO UPDATE SET
			cover_url = excluded.cover_url,
			source_key = excluded.source_key,
			found = excluded.found,
			expires_at = excluded.expires_at,
			updated_at = CURRENT_TIMESTAMP
	`, entry.CacheKey, entry.CoverURL, entry.SourceKey, entry.Found, entry.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert cover cache entry: %w", err)
	}
	return nil
}

func (r *CoverCacheRepository) Delete(cacheKey string) error {
	if _, err := r.db.Exec(`DELETE FROM cover_cache WHERE cache_key = ?`, cacheKey); err != nil {
		return fmt.Errorf("delete cover cache entry: %w", err)
	}
	return nil
}

// DeleteNegatives drops every "no cover found" entry, mirroring the in-memory
// invalidation that runs when a tracker's linked sources change.
func (r *CoverCacheRepository) DeleteNegatives() error {
	if _, err := r.db.Exec(`DELETE FROM cover_cache WHERE found = 0`); err != nil {
		return fmt.Errorf("delete negative cover entries: %w", err)
	}
	return nil
}
