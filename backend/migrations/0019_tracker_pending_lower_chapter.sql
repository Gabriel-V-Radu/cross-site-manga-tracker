ALTER TABLE trackers ADD COLUMN pending_lower_chapter REAL;
ALTER TABLE trackers ADD COLUMN pending_lower_first_seen_at DATETIME;

-- A fallback source may report a lower chapter number than the one on record for
-- two opposite reasons: the stored number is wrong, or the mirror lags. Applying
-- it immediately would let any lagging mirror walk a tracker backwards; refusing
-- it forever freezes a wrong number, which is what happened when MangaFire went
-- behind a bot challenge. Its API reported the highest chapter in *any* language,
-- so trackers were left holding a raw-Japanese number that the readable English
-- mirrors could never correct — one sat at 302 while every source said 176.
--
-- These two columns hold a lower number that has been observed but not yet
-- acted on, plus when it was first seen, so the poller can require it to persist
-- before overwriting the stored chapter. Nothing to backfill: an absent pending
-- value simply means nothing is awaiting confirmation.
