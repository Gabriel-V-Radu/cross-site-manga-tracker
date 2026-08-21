-- Which linked source a tracker's reading links should prefer. NULL = auto
-- (the poller/dashboard pick the best available). Set, it pins the chapter
-- links to one site — e.g. a MangaUpdates-primary tracker whose reader wants
-- to open MangaFire even while that site is unreachable server-side.
ALTER TABLE trackers ADD COLUMN reading_source_id INTEGER;
