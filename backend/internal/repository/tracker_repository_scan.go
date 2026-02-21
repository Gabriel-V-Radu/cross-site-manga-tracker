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

func scanTracker(scanner rowScanner) (*models.Tracker, error) {
	var tracker models.Tracker
	var relatedTitlesRaw sql.NullString
	var sourceItemID sql.NullString
	var lastReadChapter sql.NullFloat64
	var rating sql.NullFloat64
	var lastReadAt sql.NullTime
	var latestKnownChapter sql.NullFloat64
	var latestReleaseAt sql.NullTime
	var lastCheckedAt sql.NullTime

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
	if lastCheckedAt.Valid {
		tracker.LastCheckedAt = &lastCheckedAt.Time
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
