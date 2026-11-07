package repository

import (
	"database/sql"
	"time"
)

type TrackerListOptions struct {
	ProfileID int64
	Statuses  []string
	TagNames  []string
	SourceIDs []int64
	SortBy    string
	Order     string
	Query     string
	Limit     int
	Offset    int
}

type TrackerRepository struct {
	db *sql.DB
}

type PollingTracker struct {
	ID                 int64
	ProfileID          int64
	Title              string
	Status             string
	SourceID           int64
	SourceItemID       *string
	SourceURL          string
	LatestKnownChapter *float64
	LastReadChapter    *float64
	LatestReleaseAt    *time.Time
	SourceKey          string
	LastCheckedAt      *time.Time
	// PendingLowerChapter is a chapter number below the stored one that a source
	// reported but that has not been acted on yet, with the time it was first
	// seen. Together they let a downward correction be required to persist before
	// it overwrites the stored chapter.
	PendingLowerChapter     *float64
	PendingLowerFirstSeenAt *time.Time
	// LatestChapterSourceID names the source that reported the stored chapter
	// number, nil until some poll confirms it. The poller reads it to know
	// whether a confirming poll should backfill it.
	LatestChapterSourceID *int64
	// PrimaryLastPolledAt is the primary's last_polled_at, read from the
	// mirror row UpdatePollingState keeps for it in tracker_sources; nil when
	// the poller has not asked the primary since the column exists.
	PrimaryLastPolledAt *time.Time
	// AlternateSources are the tracker's other linked sources, excluding the
	// primary one above. The poller rotates each tracker's check across them
	// and the primary, and falls back to them when the source whose turn it
	// is cannot be read, so a single site going dark does not freeze the
	// tracker.
	AlternateSources []TrackerSourceRef
}

// TrackerSourceRef is one of a tracker's linked sources reduced to what a caller
// needs to read it: enough to pick a connector and resolve a URL. Used by the
// poller and by the dashboard, which both fall back to alternates when a
// tracker's primary source cannot be read.
type TrackerSourceRef struct {
	SourceID     int64
	SourceKey    string
	SourceItemID *string
	SourceURL    string
	// LastPolledAt and InStep are the poll rotation's memory of this link
	// (see migration 0004): when the poller last heard from the site about
	// this tracker, and whether that answer carried the chapter the tracker
	// then stored. Only the poller reads them.
	LastPolledAt *time.Time
	InStep       bool
}

// SourcePoll is what one poll learned about one of the tracker's linked
// sites, recorded on its tracker_sources row so the next cycle's rotation
// can tell who is due and who agrees.
type SourcePoll struct {
	// SourceID and SourceURL name the link. Primary links the tracker's
	// primary source instead, as the trackers row names it once this poll's
	// write is in: a poll that refreshed the primary's canonical URL has just
	// moved its mirror row to that URL.
	SourceID  int64
	SourceURL string
	Primary   bool
	// Answered is false for a request the site failed: it still stamps
	// last_polled_at, so the rotation moves on, but says nothing about the
	// site's numbering and leaves in_step as it was.
	Answered bool
	InStep   bool
}

// PollingUpdate is the outcome of one poll, as the repository needs to record
// it. It is a struct rather than a parameter list because the write now carries
// three separate decisions — the chapter, the release date, and any pending
// downward correction — and which of them apply depends on where the data came
// from.
type PollingUpdate struct {
	TrackerID int64

	// SnapshotSourceID, SnapshotSourceURL and SnapshotLatestKnownChapter are
	// the tracker's source_id, source_url and latest_known_chapter exactly as
	// they stood when the poll cycle loaded it. UpdatePollingState refuses to
	// write unless the row still carries all three: a cycle can run for tens
	// of minutes, and a write computed from a stale snapshot must not revert a
	// mid-cycle edit. The source_id alone missed a repoint on the same site
	// (fixing a wrong MangaUpdates match keeps source_id, and the stale poll
	// wrote the old series' URL and chapter back) and a hand repair of the
	// chapter (repair-latest-chapter, or the edit form), which the poll
	// overwrote with the junk number it had snapshotted. Unlike SourceID and
	// CurrentSourceURL they are set on fallback polls too: they identify the
	// snapshot, not the source being mirrored.
	SnapshotSourceID           int64
	SnapshotSourceURL          string
	SnapshotLatestKnownChapter *float64

	// SourceID and CurrentSourceURL mirror the primary source into
	// tracker_sources. Both are left zero by a fallback poll, which must not
	// repoint the tracker at the mirror that answered it.
	SourceID         int64
	CurrentSourceURL string
	SourceItemID     *string
	SourceURL        string

	LatestKnownChapter   *float64
	LatestReleaseAt      *time.Time
	ClearLatestReleaseAt bool

	// LatestChapterSourceID records which source reported the chapter number
	// being stored. Nil leaves whatever is recorded untouched - the right
	// value for a poll whose report did not decide the stored number.
	LatestChapterSourceID *int64

	// PendingLowerChapter records a lower chapter number awaiting confirmation.
	// Nil clears whatever was pending, which is what every poll that agrees with
	// the stored number does.
	PendingLowerChapter *float64

	// SourcePolls are the sites this poll asked, written in the same
	// transaction once the tracker's own row is: whether a site agreed is
	// measured against the chapter that write stores, so a write the
	// stale-snapshot guard skips records none of them.
	SourcePolls []SourcePoll

	CheckedAt time.Time
}

func NewTrackerRepository(db *sql.DB) *TrackerRepository {
	return &TrackerRepository{db: db}
}
