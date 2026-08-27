-- Which source reported the chapter number stored in latest_known_chapter.
-- The poller knew this every cycle and threw it away, so when no reading
-- site could resolve a chapter link the card had nothing to attribute the
-- number to and fell back to whoever supplied the cover art - the weakest
-- signal on the card. With the reporter recorded, a card whose chapter came
-- from ComicK can present ComicK (whose chapter page at least exists)
-- instead of a site that supplied nothing. NULL means no poll has reported
-- the stored number yet; the next poll that confirms it fills it in.
ALTER TABLE trackers ADD COLUMN latest_chapter_source_id INTEGER;
