ALTER TABLE trackers ADD COLUMN last_read_at DATETIME;

UPDATE trackers
SET last_read_at = updated_at
WHERE last_read_chapter IS NOT NULL
  AND last_read_at IS NULL;
