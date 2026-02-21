package repository

import (
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

func (r *SourceRepository) ListProfileSourceLogoURLs(profileID int64) (map[int64]string, error) {
	rows, err := r.db.Query(`
		SELECT source_id, logo_url
		FROM profile_source_logos
		WHERE profile_id = ?
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile source logo urls: %w", err)
	}
	defer rows.Close()

	logoBySourceID := make(map[int64]string)
	for rows.Next() {
		var sourceID int64
		var logoURL string
		if err := rows.Scan(&sourceID, &logoURL); err != nil {
			return nil, fmt.Errorf("scan profile source logo url: %w", err)
		}

		trimmedLogoURL := strings.TrimSpace(logoURL)
		if sourceID <= 0 || trimmedLogoURL == "" {
			continue
		}
		logoBySourceID[sourceID] = trimmedLogoURL
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile source logo urls: %w", err)
	}

	return logoBySourceID, nil
}

func (r *SourceRepository) UpsertProfileSourceLogoURLs(profileID int64, logoBySourceID map[int64]string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin source logo urls tx: %w", err)
	}

	for sourceID, logoURL := range logoBySourceID {
		if sourceID <= 0 {
			continue
		}

		trimmedLogoURL := strings.TrimSpace(logoURL)
		if trimmedLogoURL == "" {
			if _, err := tx.Exec(`
				DELETE FROM profile_source_logos
				WHERE profile_id = ? AND source_id = ?
			`, profileID, sourceID); err != nil {
				tx.Rollback()
				return fmt.Errorf("delete profile source logo: %w", err)
			}
			continue
		}

		if _, err := tx.Exec(`
			INSERT INTO profile_source_logos (profile_id, source_id, logo_url)
			VALUES (?, ?, ?)
			ON CONFLICT(profile_id, source_id)
			DO UPDATE SET
				logo_url = excluded.logo_url,
				updated_at = CURRENT_TIMESTAMP
		`, profileID, sourceID, trimmedLogoURL); err != nil {
			tx.Rollback()
			return fmt.Errorf("upsert profile source logo: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source logo urls tx: %w", err)
	}

	return nil
}
