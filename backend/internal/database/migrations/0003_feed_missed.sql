-- When the sites' update feeds last failed this tracker: a poll cycle found a
-- new chapter for it well after the chapter came out, although every site the
-- tracker is linked to publishes a feed that should have shown it. A tracker
-- whose sites all have feeds is otherwise only swept by the cycle every couple
-- of hours; while this stamp is recent it goes back to being asked every
-- cycle, so a title the feeds cannot see (raws running ahead on MangaFire, a
-- back-dated upload) pays for that once rather than on every chapter.
ALTER TABLE trackers ADD COLUMN feed_missed_at DATETIME;
