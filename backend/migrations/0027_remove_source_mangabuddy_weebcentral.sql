-- MangaBuddy and WeebCentral retire together, superseded by ComicK + MangaHub:
-- MangaBuddy live-reported corrupt chapter numbers that poisoned trackers and
-- lost the freshness comparison, WeebCentral was linked to a single tracker
-- and lost on the manhua/obscure tail. Disabled like 0012 rather than deleted;
-- cleanup-stale-sources removes their tracker_sources rows and, once the keys
-- are gone from the compiled registry, the source rows themselves.
UPDATE sources
SET enabled = 0,
    updated_at = CURRENT_TIMESTAMP
WHERE key IN ('mangabuddy', 'weebcentral');
