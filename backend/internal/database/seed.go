package database

import (
	"database/sql"
	"fmt"
)

// SeedDefaults inserts the source rows the compiled connectors need and the two
// profiles. It is the one list of sources that has to match the connector
// registry: the per-source migrations under migrations/ are history (they
// inserted a source when its connector arrived) and are not consulted for the
// current set. Idempotent, so it runs on every start.
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
		{key: "mangafire", name: "MangaFire", kind: "native", enabled: true},
		{key: "asuracomic", name: "AsuraComic", kind: "native", enabled: true},
		{key: "flamecomics", name: "FlameComics", kind: "native", enabled: true},
		{key: "mgeko", name: "Mgeko", kind: "native", enabled: true},
		{key: "webtoons", name: "WEBTOON", kind: "native", enabled: true},
		{key: "freewebnovel", name: "FreeWebNovel", kind: "native", enabled: true},
		{key: "mangaupdates", name: "MangaUpdates", kind: "native", enabled: true},
		{key: "comick", name: "ComicK", kind: "native", enabled: true},
		{key: "mangahub", name: "MangaHub", kind: "native", enabled: true},
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

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seed tx: %w", err)
	}

	return nil
}
