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
	SourceKey          string
	LastCheckedAt      *time.Time
}

func NewTrackerRepository(db *sql.DB) *TrackerRepository {
	return &TrackerRepository{db: db}
}
