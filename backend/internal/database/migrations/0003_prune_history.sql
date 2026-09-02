-- Housekeeping found in the 2026-09-02 review of the deployed database.

-- Link and tag rows whose tracker no longer exists. The foreign keys cascade
-- on delete, so these could only have been left behind by a delete that ran
-- with foreign_keys off (a restore or a hand edit); twelve links and two tags
-- on the deployed database, all from trackers removed in early 2026.
DELETE FROM tracker_sources
WHERE NOT EXISTS (SELECT 1 FROM trackers t WHERE t.id = tracker_sources.tracker_id);

DELETE FROM tracker_tags
WHERE NOT EXISTS (SELECT 1 FROM trackers t WHERE t.id = tracker_tags.tracker_id);

-- The ledger rows of the thirty migrations squashed into 0001_baseline.sql.
-- The runner only ever asks about the files it embeds, so the old names are
-- dead weight; the two rows this squash added stay.
DELETE FROM schema_migrations
WHERE version NOT IN ('0001_baseline.sql', '0002_source_logos_shared.sql');

-- Two columns from the first schema for a per-site YAML connector kind that
-- was never built: no row ever had a value and nothing wrote one. The
-- connector_kind CHECK still lists 'yaml' because rewriting a CHECK means
-- rebuilding the table every other table points at, which is not worth an
-- unused enum value.
ALTER TABLE sources DROP COLUMN base_url;
ALTER TABLE sources DROP COLUMN config_path;
