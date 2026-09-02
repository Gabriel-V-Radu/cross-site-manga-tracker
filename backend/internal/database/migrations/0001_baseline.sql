-- The schema as of 2026-09-02, squashed from the thirty migrations that built
-- it incrementally (0001_init through 0030_drop_dead_schema). Those files are
-- gone; git history has them.
--
-- Two properties this file has to keep:
--
--   * It is idempotent (IF NOT EXISTS throughout, no data). A database that
--     predates the squash has a schema_migrations ledger naming the thirty old
--     files and not this one, so this file runs there on top of a complete
--     schema and must change nothing.
--
--   * It carries no rows. The sources the connectors need and the two
--     profiles are inserted by SeedDefaults on every start; that Go list is
--     the one place they are defined.
--
-- Column order in trackers and cover_cache is the historical one, with the
-- columns that arrived through ALTER TABLE at the end, so a fresh database
-- matches the deployed one column for column.

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

CREATE TABLE IF NOT EXISTS profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
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
    last_read_at DATETIME,
    latest_release_at DATETIME,
    profile_id INTEGER NOT NULL DEFAULT 1,
    -- Half-star steps between 0 and 10.
    rating REAL CHECK (
        rating IS NULL
        OR (
            rating >= 0
            AND rating <= 10
            AND ABS((rating * 2) - ROUND(rating * 2)) < 0.000001
        )
    ),
    related_titles TEXT,
    -- When the recorded chapter first appeared, for sources that report a
    -- chapter number but no release date; stable, unlike last_checked_at, so
    -- the "Latest chapter" sort has something honest to rank by.
    latest_chapter_seen_at DATETIME,
    -- A lower chapter number a fallback source has reported but the poller
    -- has not acted on yet, and when it was first seen: a lagging mirror must
    -- persist before it is allowed to walk the stored number backwards.
    pending_lower_chapter REAL,
    pending_lower_first_seen_at DATETIME,
    -- Which linked source the reading links prefer. NULL = auto.
    reading_source_id INTEGER,
    -- Which source reported the number in latest_known_chapter. NULL until a
    -- poll confirms it.
    latest_chapter_source_id INTEGER,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_trackers_status ON trackers(status);
CREATE INDEX IF NOT EXISTS idx_trackers_source_id ON trackers(source_id);
CREATE INDEX IF NOT EXISTS idx_trackers_profile_id ON trackers(profile_id);

-- Every site a tracker is linked to beyond its primary source.
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

CREATE INDEX IF NOT EXISTS idx_tracker_sources_source_id ON tracker_sources(source_id);

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

CREATE UNIQUE INDEX IF NOT EXISTS uq_custom_tags_profile_icon_key ON custom_tags(profile_id, icon_key);

CREATE TABLE IF NOT EXISTS tracker_tags (
    tracker_id INTEGER NOT NULL,
    tag_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tracker_id, tag_id),
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id) REFERENCES custom_tags(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tracker_tags_tag_id ON tracker_tags(tag_id);

-- Per-profile site logos. Replaced by the shared source_logos table in 0002;
-- it is created here so that migration has the same table to convert on a
-- fresh database as on a deployed one.
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

CREATE INDEX IF NOT EXISTS idx_profile_source_logos_source_id ON profile_source_logos(source_id);

-- Candidates found by scanning a source for trackers that lack a link to it,
-- awaiting review in the dashboard. Decided rows are kept: a rejected
-- candidate must not resurface on the next scan, and an accepted one documents
-- where the link came from. A row with an empty candidate_url is a
-- tracker-level marker ("dismissed": reviewed, no match exists on this source)
-- that takes the tracker out of future scans and out of the review queue.
CREATE TABLE IF NOT EXISTS source_link_suggestions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tracker_id INTEGER NOT NULL,
    source_id INTEGER NOT NULL,
    candidate_url TEXT NOT NULL DEFAULT '',
    candidate_item_id TEXT,
    candidate_title TEXT NOT NULL DEFAULT '',
    candidate_cover_url TEXT,
    candidate_latest_chapter REAL,
    candidate_release_at DATETIME,
    score REAL NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at DATETIME,
    UNIQUE (tracker_id, source_id, candidate_url),
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_source_link_suggestions_source_status ON source_link_suggestions(source_id, status);

-- Resolved cover art, persisted so a restart does not re-resolve every card
-- against the sources' rate-limited APIs. Negative entries ("no cover found")
-- are stored too and expire on their own. local_path names a downloaded copy
-- under data/covers; an entry that has one is served from this host and never
-- expires. Empty means remote-only, which still expires on its TTL.
CREATE TABLE IF NOT EXISTS cover_cache (
    cache_key TEXT PRIMARY KEY,
    cover_url TEXT NOT NULL DEFAULT '',
    source_key TEXT NOT NULL DEFAULT '',
    found INTEGER NOT NULL DEFAULT 0,
    expires_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    local_path TEXT NOT NULL DEFAULT ''
);
