package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type SourceRepository struct {
	db *sql.DB
}

func NewSourceRepository(db *sql.DB) *SourceRepository {
	return &SourceRepository{db: db}
}

func (r *SourceRepository) ListEnabled(ctx context.Context) ([]models.Source, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, key, name, connector_kind, base_url, config_path, enabled, created_at, updated_at
		FROM sources
		WHERE enabled = 1
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list enabled sources: %w", err)
	}
	defer rows.Close()

	items := make([]models.Source, 0)
	for rows.Next() {
		var source models.Source
		var baseURL sql.NullString
		var configPath sql.NullString
		var enabled bool
		if err := rows.Scan(
			&source.ID,
			&source.Key,
			&source.Name,
			&source.ConnectorKind,
			&baseURL,
			&configPath,
			&enabled,
			&source.CreatedAt,
			&source.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		source.Enabled = enabled
		if baseURL.Valid {
			source.BaseURL = &baseURL.String
		}
		if configPath.Valid {
			source.ConfigPath = &configPath.String
		}
		items = append(items, source)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sources: %w", err)
	}

	return items, nil
}

func (r *SourceRepository) GetByID(ctx context.Context, id int64) (*models.Source, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, key, name, connector_kind, base_url, config_path, enabled, created_at, updated_at
		FROM sources
		WHERE id = ?
	`, id)

	return scanSource(row, "get source by id")
}

func (r *SourceRepository) GetByKey(ctx context.Context, key string) (*models.Source, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, key, name, connector_kind, base_url, config_path, enabled, created_at, updated_at
		FROM sources
		WHERE key = ?
	`, strings.TrimSpace(strings.ToLower(key)))

	return scanSource(row, "get source by key")
}

// scanSource reads one source row, reporting a missing row as (nil, nil) the
// way both lookups have always done.
func scanSource(row rowScanner, operation string) (*models.Source, error) {
	var source models.Source
	var baseURL sql.NullString
	var configPath sql.NullString
	var enabled bool
	if err := row.Scan(
		&source.ID,
		&source.Key,
		&source.Name,
		&source.ConnectorKind,
		&baseURL,
		&configPath,
		&enabled,
		&source.CreatedAt,
		&source.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", operation, err)
	}

	source.Enabled = enabled
	if baseURL.Valid {
		source.BaseURL = &baseURL.String
	}
	if configPath.Valid {
		source.ConfigPath = &configPath.String
	}

	return &source, nil
}

// ListSourceLogoURLs is the uploaded logo of every source that has one, keyed
// by source id. Logos are not per-profile: one upload shows on every profile.
func (r *SourceRepository) ListSourceLogoURLs(ctx context.Context) (map[int64]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT source_id, logo_url
		FROM source_logos
	`)
	if err != nil {
		return nil, fmt.Errorf("list source logo urls: %w", err)
	}
	defer rows.Close()

	logoBySourceID := make(map[int64]string)
	for rows.Next() {
		var sourceID int64
		var logoURL string
		if err := rows.Scan(&sourceID, &logoURL); err != nil {
			return nil, fmt.Errorf("scan source logo url: %w", err)
		}

		trimmedLogoURL := strings.TrimSpace(logoURL)
		if sourceID <= 0 || trimmedLogoURL == "" {
			continue
		}
		logoBySourceID[sourceID] = trimmedLogoURL
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source logo urls: %w", err)
	}

	return logoBySourceID, nil
}

// UpsertSourceLogoURLs stores the given logo per source; an empty URL removes
// the source's logo.
func (r *SourceRepository) UpsertSourceLogoURLs(ctx context.Context, logoBySourceID map[int64]string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin source logo urls tx: %w", err)
	}
	defer tx.Rollback()

	for sourceID, logoURL := range logoBySourceID {
		if sourceID <= 0 {
			continue
		}

		trimmedLogoURL := strings.TrimSpace(logoURL)
		if trimmedLogoURL == "" {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM source_logos
				WHERE source_id = ?
			`, sourceID); err != nil {
				return fmt.Errorf("delete source logo: %w", err)
			}
			continue
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO source_logos (source_id, logo_url)
			VALUES (?, ?)
			ON CONFLICT(source_id)
			DO UPDATE SET
				logo_url = excluded.logo_url,
				updated_at = CURRENT_TIMESTAMP
		`, sourceID, trimmedLogoURL); err != nil {
			return fmt.Errorf("upsert source logo: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source logo urls tx: %w", err)
	}

	return nil
}
