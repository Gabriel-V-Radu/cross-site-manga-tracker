-- The cover cache used to hold only URLs: the browser hotlinked each cover
-- from the source site's CDN on every render, and when the TTL expired the
-- next render re-resolved it against the sources' rate-limited APIs - a
-- visible reload of every card's art. local_path names a downloaded copy
-- under data/covers; an entry that has one serves the image from this host,
-- never expires, and survives the source site going dark or retiring. Empty
-- means remote-only (the old behavior), which still expires on its TTL.
ALTER TABLE cover_cache ADD COLUMN local_path TEXT NOT NULL DEFAULT '';
