-- Site logos used to be stored per profile: the table was keyed by
-- (profile_id, source_id), the upload form saved for the active profile only,
-- and the file name carried the profile id. Both profiles see the same ten
-- sites, so making them look alike meant uploading every logo twice; in
-- practice the second profile was never done and rendered text badges. One
-- logo per source from here on, shown to every profile.
CREATE TABLE IF NOT EXISTS source_logos (
    source_id INTEGER PRIMARY KEY,
    logo_url TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
);

-- Where both profiles had uploaded a logo for the same site the lower profile
-- id wins and the other row's file stays on disk unreferenced. On the deployed
-- database only profile 1 ever had logos, so nothing is lost there.
INSERT OR IGNORE INTO source_logos (source_id, logo_url, created_at, updated_at)
SELECT psl.source_id, psl.logo_url, psl.created_at, psl.updated_at
FROM profile_source_logos psl
WHERE psl.profile_id = (
    SELECT MIN(other.profile_id)
    FROM profile_source_logos other
    WHERE other.source_id = psl.source_id
);

DROP TABLE IF EXISTS profile_source_logos;
