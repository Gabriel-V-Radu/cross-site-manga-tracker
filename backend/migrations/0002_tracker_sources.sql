CREATE TABLE IF NOT EXISTS tracker_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tracker_id INTEGER NOT NULL,
    source_id INTEGER NOT NULL,
    source_item_id TEXT,
    source_url TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (tracker_id, source_id, source_url),
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_tracker_sources_tracker_id ON tracker_sources(tracker_id);
CREATE INDEX IF NOT EXISTS idx_tracker_sources_source_id ON tracker_sources(source_id);