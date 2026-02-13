CREATE TABLE IF NOT EXISTS sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    connector_kind TEXT NOT NULL CHECK (connector_kind IN ('native', 'yaml')),
    base_url TEXT,
    config_path TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS trackers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    source_id INTEGER NOT NULL,
    source_item_id TEXT,
    source_url TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('reading', 'completed', 'on_hold', 'dropped', 'plan_to_read')),
    last_read_chapter REAL,
    latest_known_chapter REAL,
    last_checked_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS chapters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tracker_id INTEGER NOT NULL,
    chapter_number REAL,
    chapter_label TEXT,
    chapter_url TEXT,
    released_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (tracker_id, chapter_number, chapter_url),
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_trackers_status ON trackers(status);
CREATE INDEX IF NOT EXISTS idx_trackers_source_id ON trackers(source_id);
CREATE INDEX IF NOT EXISTS idx_chapters_tracker_id ON chapters(tracker_id);
