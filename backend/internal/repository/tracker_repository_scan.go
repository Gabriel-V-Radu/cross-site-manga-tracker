package repository

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
)

type rowScanner interface {
	Scan(dest ...any) error
}

// trackerColumns is the select list scanTracker reads, in its order. It used to
// be spelled out at each query, and a column added to one copy and not the
// other was a scan error waiting for the first request.
const trackerColumns = `
	trackers.id, trackers.profile_id, trackers.title, trackers.related_titles, trackers.source_id,
	trackers.source_item_id, trackers.source_url, trackers.status, trackers.last_read_chapter,
	trackers.rating, trackers.last_read_at, trackers.latest_known_chapter, trackers.latest_release_at,
	trackers.latest_chapter_seen_at, trackers.latest_chapter_source_id, trackers.last_checked_at,
	trackers.reading_source_id, trackers.created_at, trackers.updated_at,
	trackers.latest_chapter_found_at
`

func scanTracker(scanner rowScanner) (*models.Tracker, error) {
	var tracker models.Tracker
	var relatedTitlesRaw sql.NullString
	var sourceItemID sql.NullString
	var lastReadChapter sql.NullFloat64
	var rating sql.NullFloat64
	var lastReadAt sql.NullTime
	var latestKnownChapter sql.NullFloat64
	var latestReleaseAt sql.NullTime
	var latestChapterSeenAt sql.NullTime
	var latestChapterSourceID sql.NullInt64
	var lastCheckedAt sql.NullTime
	var readingSourceID sql.NullInt64
	var latestChapterFoundAt sql.NullTime

	// trackerColumns lists these columns in this order.
	err := scanner.Scan(
		&tracker.ID,
		&tracker.ProfileID,
		&tracker.Title,
		&relatedTitlesRaw,
		&tracker.SourceID,
		&sourceItemID,
		&tracker.SourceURL,
		&tracker.Status,
		&lastReadChapter,
		&rating,
		&lastReadAt,
		&latestKnownChapter,
		&latestReleaseAt,
		&latestChapterSeenAt,
		&latestChapterSourceID,
		&lastCheckedAt,
		&readingSourceID,
		&tracker.CreatedAt,
		&tracker.UpdatedAt,
		&latestChapterFoundAt,
	)
	if err != nil {
		return nil, err
	}
	if latestChapterFoundAt.Valid {
		tracker.LatestChapterFoundAt = &latestChapterFoundAt.Time
	}

	if sourceItemID.Valid {
		tracker.SourceItemID = &sourceItemID.String
	}
	if relatedTitlesRaw.Valid {
		decodedRelatedTitles := decodeRelatedTitlesJSON(relatedTitlesRaw.String)
		tracker.RelatedTitles = sanitizeRelatedTitles(decodedRelatedTitles)
	}
	if lastReadChapter.Valid {
		tracker.LastReadChapter = &lastReadChapter.Float64
	}
	if rating.Valid {
		tracker.Rating = &rating.Float64
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
	if latestChapterSeenAt.Valid {
		tracker.LatestChapterSeenAt = &latestChapterSeenAt.Time
	}
	if latestChapterSourceID.Valid && latestChapterSourceID.Int64 > 0 {
		tracker.LatestChapterSourceID = &latestChapterSourceID.Int64
	}
	if lastCheckedAt.Valid {
		tracker.LastCheckedAt = &lastCheckedAt.Time
	}
	if readingSourceID.Valid && readingSourceID.Int64 > 0 {
		tracker.ReadingSourceID = &readingSourceID.Int64
	}

	return &tracker, nil
}

func encodeRelatedTitlesJSON(values []string) *string {
	sanitized := sanitizeRelatedTitles(values)
	if len(sanitized) == 0 {
		return nil
	}

	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return nil
	}
	raw := string(encoded)
	return &raw
}

func decodeRelatedTitlesJSON(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil
	}

	return values
}

func sanitizeRelatedTitles(values []string) []string {
	return searchutil.FilterEnglishAlphabetNames(values)
}
