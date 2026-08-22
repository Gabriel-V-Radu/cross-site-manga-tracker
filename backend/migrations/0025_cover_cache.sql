-- Covers used to live only in an in-memory cache, so every container restart
-- forgot all of them and the next dashboard render re-resolved every card
-- against the sources' rate-limited APIs: dozens of requests pacing through
-- the shared per-host throttle, slow for the viewer and starving the poller
-- of the same hosts' slots. Persisting the entries makes a cover a
-- once-per-TTL lookup instead of a once-per-restart storm. Negative entries
-- ("no cover found") are persisted too — they expire on their own and are
-- dropped when a tracker's links change, same as in memory.
CREATE TABLE IF NOT EXISTS cover_cache (
    cache_key TEXT PRIMARY KEY,
    cover_url TEXT NOT NULL DEFAULT '',
    source_key TEXT NOT NULL DEFAULT '',
    found INTEGER NOT NULL DEFAULT 0,
    expires_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
