package repository

import (
	"database/sql"
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
	SourceURL          string
	LatestKnownChapter *float64
	SourceKey          string
}

func NewTrackerRepository(db *sql.DB) *TrackerRepository {
	return &TrackerRepository{db: db}
}
