CREATE TABLE IF NOT EXISTS profile_source_logos (
    profile_id INTEGER NOT NULL,
    source_id INTEGER NOT NULL,
    logo_url TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (profile_id, source_id),
    FOREIGN KEY (profile_id) REFERENCES profiles(id) ON DELETE CASCADE,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_profile_source_logos_profile_id ON profile_source_logos(profile_id);
CREATE INDEX IF NOT EXISTS idx_profile_source_logos_source_id ON profile_source_logos(source_id);
