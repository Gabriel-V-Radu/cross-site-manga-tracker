package repository

import (
	"database/sql"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTracker(scanner rowScanner) (*models.Tracker, error) {
	var tracker models.Tracker
	var sourceItemID sql.NullString
	var lastReadChapter sql.NullFloat64
	var lastReadAt sql.NullTime
	var latestKnownChapter sql.NullFloat64
	var latestReleaseAt sql.NullTime
	var lastCheckedAt sql.NullTime

	err := scanner.Scan(
		&tracker.ID,
		&tracker.ProfileID,
		&tracker.Title,
		&tracker.SourceID,
		&sourceItemID,
		&tracker.SourceURL,
		&tracker.Status,
		&lastReadChapter,
		&lastReadAt,
		&latestKnownChapter,
		&latestReleaseAt,
		&lastCheckedAt,
		&tracker.CreatedAt,
		&tracker.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if sourceItemID.Valid {
		tracker.SourceItemID = &sourceItemID.String
	}
	if lastReadChapter.Valid {
		tracker.LastReadChapter = &lastReadChapter.Float64
	}
	if lastReadAt.Valid {
		tracker.LastReadAt = &lastReadAt.Time
	}
	if latestKnownChapter.Valid {
		tracker.LatestKnownChapter = &latestKnownChapter.Float64
	}
	if latestReleaseAt.Valid {
		tracker.LatestReleaseAt = &latestReleaseAt.Time
	}
	if lastCheckedAt.Valid {
		tracker.LastCheckedAt = &lastCheckedAt.Time
	}

	return &tracker, nil
}
