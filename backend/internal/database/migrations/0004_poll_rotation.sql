-- The poller stopped asking every tracker's primary source on every cycle: it
-- now rotates each tracker's check across its linked sources, asking the one
-- it asked longest ago (scheduler.planPoll). Both columns are that rotation's
-- memory, per tracker and per linked site; the primary takes part through the
-- mirror row UpdatePollingState keeps for it.
--
-- last_polled_at: when the poller last got an answer or an error from this
-- site for this tracker. A request a cooldown refused locally never reached
-- the site and does not count.
--
-- in_step: whether the site's last answer carried exactly the chapter number
-- the tracker stored after that poll. An alternate that is not in step may
-- take its turn, but a higher number from it is not stored on its word alone:
-- the poll asks the next site first, so a site that numbers chapters its own
-- way cannot push the tracker ahead and then hold it there by agreeing with
-- itself every time its turn comes back.
ALTER TABLE tracker_sources ADD COLUMN last_polled_at DATETIME;
ALTER TABLE tracker_sources ADD COLUMN in_step INTEGER NOT NULL DEFAULT 0;
