package database

import (
	"database/sql"
	"fmt"
)

func SeedDefaults(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin seed tx: %w", err)
	}

	defaultSources := []struct {
		key     string
		name    string
		kind    string
		enabled bool
	}{
		{key: "mangadex", name: "MangaDex", kind: "native", enabled: true},
		{key: "mangaplus", name: "MangaPlus", kind: "native", enabled: true},
		{key: "mangafire", name: "MangaFire", kind: "native", enabled: true},
		{key: "asuracomic", name: "AsuraComic", kind: "native", enabled: true},
		{key: "flamecomics", name: "FlameComics", kind: "native", enabled: true},
		{key: "webtoons", name: "WEBTOON", kind: "native", enabled: true},
	}

	for _, source := range defaultSources {
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO sources (key, name, connector_kind, enabled)
			VALUES (?, ?, ?, ?)
		`, source.key, source.name, source.kind, source.enabled)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("seed source %s: %w", source.key, err)
		}
	}

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO profiles (id, key, name)
		VALUES
			(1, 'profile1', 'Profile 1'),
			(2, 'profile2', 'Profile 2');
	`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("seed profiles: %w", err)
	}

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO settings (key, value)
		VALUES
			('polling_minutes', '30');
	`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("seed settings: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seed tx: %w", err)
	}

	return nil
}
