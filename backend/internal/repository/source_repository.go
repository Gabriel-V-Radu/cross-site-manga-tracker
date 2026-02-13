package repository

import (
	"database/sql"
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type SourceRepository struct {
	db *sql.DB
}

func NewSourceRepository(db *sql.DB) *SourceRepository {
	return &SourceRepository{db: db}
}

func (r *SourceRepository) ListEnabled() ([]models.Source, error) {
	rows, err := r.db.Query(`
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

func (r *SourceRepository) GetByID(id int64) (*models.Source, error) {
	row := r.db.QueryRow(`
		SELECT id, key, name, connector_kind, base_url, config_path, enabled, created_at, updated_at
		FROM sources
		WHERE id = ?
	`, id)

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
		return nil, fmt.Errorf("get source by id: %w", err)
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
