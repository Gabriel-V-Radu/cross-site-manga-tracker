-- What a Link Review candidate rests on, and what it would replace.
--
-- basis names the site whose own page links the candidate as the same series
-- (MangaFire's title record names its MangaDex and MangaUpdates pages); empty
-- for a candidate found by title. A candidate with a basis is an id match, not
-- a title guess, and the review card says so.
--
-- replaces_url is set on a candidate for a site the tracker is already linked
-- to: another site's link says the series lives at the candidate URL, not at
-- the stored link (a light novel linked where the manga was meant). Accepting
-- it swaps the stored link for the candidate; rejecting it keeps the link.
ALTER TABLE source_link_suggestions ADD COLUMN basis TEXT NOT NULL DEFAULT '';
ALTER TABLE source_link_suggestions ADD COLUMN replaces_url TEXT;
