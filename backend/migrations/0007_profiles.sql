CREATE TABLE IF NOT EXISTS profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO profiles (id, key, name)
VALUES
    (1, 'profile1', 'Profile 1'),
    (2, 'profile2', 'Profile 2');

ALTER TABLE trackers ADD COLUMN profile_id INTEGER NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS idx_trackers_profile_id ON trackers(profile_id);
