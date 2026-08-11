ALTER TABLE trackers ADD COLUMN latest_chapter_seen_at DATETIME;

-- When the recorded chapter first appeared, for the sources that report a
-- chapter number but no release date at all: MangaDex serves lastChapter for a
-- licensed or finished series whose English feed is empty, and MangaBuddy
-- reports no usable dates. Without this the "Latest chapter" sort fell back to
-- last_checked_at/updated_at, both of which every poll rewrites, so a tracker
-- with no release date was ranked by when it was last polled and sat near the
-- top of the library forever.
--
-- Existing rows carry no record of when their chapter appeared. The release
-- date is that record wherever a source supplied one; where none did, the date
-- the tracker was added is the only honest bound available, and it is stable,
-- which is the property the sort needs.
UPDATE trackers
SET latest_chapter_seen_at = COALESCE(latest_release_at, created_at)
WHERE latest_known_chapter IS NOT NULL;
