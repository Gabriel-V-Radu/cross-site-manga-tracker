CREATE TABLE IF NOT EXISTS custom_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id INTEGER NOT NULL,
    name TEXT NOT NULL COLLATE NOCASE,
    icon_key TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (profile_id, name),
    FOREIGN KEY (profile_id) REFERENCES profiles(id) ON DELETE CASCADE,
    CHECK (icon_key IS NULL OR icon_key IN ('icon_1', 'icon_2', 'icon_3'))
);

CREATE TABLE IF NOT EXISTS tracker_tags (
    tracker_id INTEGER NOT NULL,
    tag_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tracker_id, tag_id),
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id) REFERENCES custom_tags(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_custom_tags_profile_id ON custom_tags(profile_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_custom_tags_profile_icon_key ON custom_tags(profile_id, icon_key);
CREATE INDEX IF NOT EXISTS idx_tracker_tags_tracker_id ON tracker_tags(tracker_id);
CREATE INDEX IF NOT EXISTS idx_tracker_tags_tag_id ON tracker_tags(tag_id);
