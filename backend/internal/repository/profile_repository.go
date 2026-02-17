package repository

import (
	"database/sql"
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type ProfileRepository struct {
	db *sql.DB
}

func NewProfileRepository(db *sql.DB) *ProfileRepository {
	return &ProfileRepository{db: db}
}

func (r *ProfileRepository) List() ([]models.Profile, error) {
	rows, err := r.db.Query(`
		SELECT id, key, name, created_at, updated_at
		FROM profiles
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()

	items := make([]models.Profile, 0)
	for rows.Next() {
		var item models.Profile
		if err := rows.Scan(&item.ID, &item.Key, &item.Name, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profiles: %w", err)
	}

	return items, nil
}

func (r *ProfileRepository) GetByID(id int64) (*models.Profile, error) {
	row := r.db.QueryRow(`
		SELECT id, key, name, created_at, updated_at
		FROM profiles
		WHERE id = ?
	`, id)

	var item models.Profile
	if err := row.Scan(&item.ID, &item.Key, &item.Name, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get profile by id: %w", err)
	}

	return &item, nil
}

func (r *ProfileRepository) GetByKey(key string) (*models.Profile, error) {
	row := r.db.QueryRow(`
		SELECT id, key, name, created_at, updated_at
		FROM profiles
		WHERE key = ?
	`, key)

	var item models.Profile
	if err := row.Scan(&item.ID, &item.Key, &item.Name, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get profile by key: %w", err)
	}

	return &item, nil
}

func (r *ProfileRepository) GetDefault() (*models.Profile, error) {
	row := r.db.QueryRow(`
		SELECT id, key, name, created_at, updated_at
		FROM profiles
		ORDER BY id ASC
		LIMIT 1
	`)

	var item models.Profile
	if err := row.Scan(&item.ID, &item.Key, &item.Name, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get default profile: %w", err)
	}

	return &item, nil
}

func (r *ProfileRepository) Rename(id int64, name string) (bool, error) {
	result, err := r.db.Exec(`
		UPDATE profiles
		SET name = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND name IS NOT ?
	`, name, id, name)
	if err != nil {
		return false, fmt.Errorf("rename profile: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("profile rename rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}
