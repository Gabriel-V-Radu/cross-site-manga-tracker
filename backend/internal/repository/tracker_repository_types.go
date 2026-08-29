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
	// AlternateSources are the tracker's other linked sources, excluding the
	// primary one above. The poller falls back to them when the primary source
	// cannot be read, so a single site going dark does not freeze the tracker.
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
}

// PollingUpdate is the outcome of one poll, as the repository needs to record
// it. It is a struct rather than a parameter list because the write now carries
// three separate decisions — the chapter, the release date, and any pending
// downward correction — and which of them apply depends on where the data came
// from.
type PollingUpdate struct {
	TrackerID int64

	// SnapshotSourceID is the tracker's source_id as it stood when the poll
	// cycle loaded it. UpdatePollingState refuses to write unless the row
	// still carries this source: a cycle can run for tens of minutes, and a
	// write computed from a stale snapshot must not revert a mid-cycle user
	// edit. Unlike SourceID it is set on fallback polls too — it identifies
	// the snapshot, not the source being mirrored.
	SnapshotSourceID int64

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

	CheckedAt time.Time
}

func NewTrackerRepository(db *sql.DB) *TrackerRepository {
	return &TrackerRepository{db: db}
}
