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
	LatestReleaseAt    *time.Time
	SourceKey          string
	LastCheckedAt      *time.Time
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

func NewTrackerRepository(db *sql.DB) *TrackerRepository {
	return &TrackerRepository{db: db}
}
