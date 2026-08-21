-- Candidates found by scanning a source for trackers that lack a link to it,
-- awaiting review in the dashboard. Decided rows are kept: a rejected candidate
-- must not resurface on the next scan, and an accepted one documents where the
-- link came from. A row with an empty candidate_url is a tracker-level marker
-- ("dismissed": reviewed, no match exists on this source) that takes the
-- tracker out of future scans and out of the review queue.
CREATE TABLE IF NOT EXISTS source_link_suggestions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tracker_id INTEGER NOT NULL,
    source_id INTEGER NOT NULL,
    candidate_url TEXT NOT NULL DEFAULT '',
    candidate_item_id TEXT,
    candidate_title TEXT NOT NULL DEFAULT '',
    candidate_cover_url TEXT,
    candidate_latest_chapter REAL,
    candidate_release_at DATETIME,
    score REAL NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at DATETIME,
    UNIQUE (tracker_id, source_id, candidate_url),
    FOREIGN KEY (tracker_id) REFERENCES trackers(id) ON DELETE CASCADE,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_source_link_suggestions_tracker ON source_link_suggestions(tracker_id);
CREATE INDEX IF NOT EXISTS idx_source_link_suggestions_source_status ON source_link_suggestions(source_id, status);
