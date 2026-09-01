-- Two tables from the first migration that nothing has ever read or written:
-- chapters (a per-chapter history the poller never recorded; it stores one
-- latest number per tracker) and settings (polling_minutes was seeded there
-- and read from the environment instead). Their Go models go with them.
DROP TABLE IF EXISTS chapters;
DROP TABLE IF EXISTS settings;

-- Indexes that duplicate the leading column of a UNIQUE constraint or primary
-- key on the same table. SQLite already keeps an index for each of those, so
-- these cost a write per insert and serve no query.
DROP INDEX IF EXISTS idx_tracker_sources_tracker_id;       -- UNIQUE (tracker_id, source_id, source_url)
DROP INDEX IF EXISTS idx_custom_tags_profile_id;           -- UNIQUE (profile_id, name)
DROP INDEX IF EXISTS idx_tracker_tags_tracker_id;          -- PRIMARY KEY (tracker_id, tag_id)
DROP INDEX IF EXISTS idx_profile_source_logos_profile_id;  -- PRIMARY KEY (profile_id, source_id)
DROP INDEX IF EXISTS idx_source_link_suggestions_tracker;  -- UNIQUE (tracker_id, source_id, candidate_url)
