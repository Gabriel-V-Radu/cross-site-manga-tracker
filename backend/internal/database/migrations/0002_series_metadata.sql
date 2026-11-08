-- Series metadata (type, genres, synopsis…) and the two things it feeds: the
-- dashboard's info panel and filters, and the poller's slower cadence for
-- series that are finished, on hiatus or long quiet.

-- What one site said about one series, as connectors.SeriesMetadata JSON.
-- Keyed by the site and the series URL rather than by tracker: two profiles
-- tracking the same series share the row, and a tracker's consolidated view is
-- rebuilt from the rows of whichever sites it is linked to. source_url holds
-- LOWER(TRIM(url)), the same normalisation tracker_sources is matched with.
-- The poller refreshes a row at most about once a week (metadata.Service).
CREATE TABLE IF NOT EXISTS source_metadata (
    source_id INTEGER NOT NULL,
    source_url TEXT NOT NULL,
    payload TEXT NOT NULL,
    fetched_at DATETIME NOT NULL,
    PRIMARY KEY (source_id, source_url),
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
);

-- One tracker's metadata consolidated across its linked sites. series_type,
-- series_status and genres (a JSON array of canonical genre names) are
-- columns because the tracker list filters on them and the poller reads the
-- status; payload is the whole consolidated view for the info panel.
-- signature names the source rows it was built from, so a link added or
-- removed, or a source row refreshed, is noticed and the row rebuilt.
CREATE TABLE IF NOT EXISTS tracker_metadata (
    tracker_id INTEGER PRIMARY KEY,
    series_type TEXT NOT NULL DEFAULT '',
    series_status TEXT NOT NULL DEFAULT '',
    genres TEXT NOT NULL DEFAULT '[]',
    payload TEXT NOT NULL DEFAULT '{}',
    signature TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE
);

-- When a poll found the chapter in latest_known_chapter, set only when a poll
-- moves the number up (never on creation or a manual edit, cleared when the
-- number is corrected down). The "Latest chapter" sort ranks by the later of
-- this and the release date, so a chapter found long after it came out — a
-- slowly polled finished series that got an extra — still lands on top.
ALTER TABLE trackers ADD COLUMN latest_chapter_found_at DATETIME;
