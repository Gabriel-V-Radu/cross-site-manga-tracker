package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Suggestion lifecycle. Pending rows are the review queue; decided rows are
// kept so a rejected candidate cannot resurface on the next scan. "Dismissed"
// lives on a marker row with an empty candidate_url and means the whole
// tracker was reviewed and has no match on that source.
const (
	LinkSuggestionPending   = "pending"
	LinkSuggestionAccepted  = "accepted"
	LinkSuggestionRejected  = "rejected"
	LinkSuggestionDismissed = "dismissed"
)

type LinkSuggestion struct {
	ID                     int64
	TrackerID              int64
	SourceID               int64
	CandidateURL           string
	CandidateItemID        *string
	CandidateTitle         string
	CandidateCoverURL      *string
	CandidateLatestChapter *float64
	CandidateReleaseAt     *time.Time
	Score                  float64
	Status                 string
	CreatedAt              time.Time
}

// LinkScanTarget is a tracker a scan should look up on a source: it has no
// link to that source yet, no accepted suggestion, and was not dismissed.
type LinkScanTarget struct {
	TrackerID          int64
	Title              string
	RelatedTitles      []string
	LatestKnownChapter *float64
}

// LinkReviewTracker is one tracker in the review queue, with whatever pending
// candidates a scan left for it (none, for the "no candidate found" section).
type LinkReviewTracker struct {
	TrackerID          int64
	Title              string
	Status             string
	SourceID           int64
	SourceURL          string
	LatestKnownChapter *float64
	LatestReleaseAt    *time.Time
	Suggestions        []LinkSuggestion
}

type LinkSuggestionRepository struct {
	db *sql.DB
}

func NewLinkSuggestionRepository(db *sql.DB) *LinkSuggestionRepository {
	return &LinkSuggestionRepository{db: db}
}

// linkableTrackersQuery selects a profile's trackers that still lack the given
// source: not their primary, not already linked, not dismissed, no accepted
// suggestion. Parameters: source, source, profile, source.
const linkableTrackersQuery = `
	SELECT t.id, t.title, t.related_titles, t.status, t.source_id, t.source_url,
	       t.latest_known_chapter, t.latest_release_at
	FROM trackers t
	WHERE t.source_id != ?
	  AND NOT EXISTS (
	      SELECT 1 FROM tracker_sources ts
	      WHERE ts.tracker_id = t.id AND ts.source_id = ?
	  )
	  AND t.profile_id = ?
	  AND NOT EXISTS (
	      SELECT 1 FROM source_link_suggestions marker
	      WHERE marker.tracker_id = t.id AND marker.source_id = ?
	        AND marker.status IN ('dismissed', 'accepted')
	  )
`

// ListScanTargets returns the trackers a scan of sourceID should look up.
func (r *LinkSuggestionRepository) ListScanTargets(profileID int64, sourceID int64) ([]LinkScanTarget, error) {
	rows, err := r.db.Query(linkableTrackersQuery+` ORDER BY t.title ASC`,
		sourceID, sourceID, profileID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("list link scan targets: %w", err)
	}
	defer rows.Close()

	targets := make([]LinkScanTarget, 0)
	for rows.Next() {
		var target LinkScanTarget
		var relatedTitlesRaw sql.NullString
		var status, sourceURL string
		var trackerSourceID int64
		var latestReleaseAt sql.NullTime
		if err := rows.Scan(
			&target.TrackerID,
			&target.Title,
			&relatedTitlesRaw,
			&status,
			&trackerSourceID,
			&sourceURL,
			&target.LatestKnownChapter,
			&latestReleaseAt,
		); err != nil {
			return nil, fmt.Errorf("scan link scan target: %w", err)
		}
		if relatedTitlesRaw.Valid {
			target.RelatedTitles = sanitizeRelatedTitles(decodeRelatedTitlesJSON(relatedTitlesRaw.String))
		}
		targets = append(targets, target)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate link scan targets: %w", err)
	}
	return targets, nil
}

// ReplacePendingSuggestions swaps a tracker's pending candidates for the given
// set. Decided rows are untouched, and a candidate that was already rejected
// stays rejected — the insert skips it rather than resurrecting it.
func (r *LinkSuggestionRepository) ReplacePendingSuggestions(trackerID int64, sourceID int64, suggestions []LinkSuggestion) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replace pending suggestions tx: %w", err)
	}

	if _, err := tx.Exec(`
		DELETE FROM source_link_suggestions
		WHERE tracker_id = ? AND source_id = ? AND status = 'pending'
	`, trackerID, sourceID); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete pending suggestions: %w", err)
	}

	for _, suggestion := range suggestions {
		candidateURL := strings.TrimSpace(suggestion.CandidateURL)
		if candidateURL == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO source_link_suggestions
				(tracker_id, source_id, candidate_url, candidate_item_id, candidate_title,
				 candidate_cover_url, candidate_latest_chapter, candidate_release_at, score, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')
			ON CONFLICT(tracker_id, source_id, candidate_url) DO NOTHING
		`, trackerID, sourceID, candidateURL, suggestion.CandidateItemID, suggestion.CandidateTitle,
			suggestion.CandidateCoverURL, suggestion.CandidateLatestChapter, suggestion.CandidateReleaseAt,
			suggestion.Score); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert suggestion: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace pending suggestions tx: %w", err)
	}
	return nil
}

// ListReviewQueue returns every tracker of the profile that still lacks the
// source, with its pending candidates. Trackers whose Suggestions are empty
// form the "no candidate found" section of the queue.
func (r *LinkSuggestionRepository) ListReviewQueue(profileID int64, sourceID int64) ([]LinkReviewTracker, error) {
	rows, err := r.db.Query(linkableTrackersQuery+` ORDER BY t.title ASC`,
		sourceID, sourceID, profileID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("list review queue trackers: %w", err)
	}
	defer rows.Close()

	queue := make([]LinkReviewTracker, 0)
	byTracker := map[int64]int{}
	for rows.Next() {
		var item LinkReviewTracker
		var relatedTitlesRaw sql.NullString
		var latestReleaseAt sql.NullTime
		if err := rows.Scan(
			&item.TrackerID,
			&item.Title,
			&relatedTitlesRaw,
			&item.Status,
			&item.SourceID,
			&item.SourceURL,
			&item.LatestKnownChapter,
			&latestReleaseAt,
		); err != nil {
			return nil, fmt.Errorf("scan review queue tracker: %w", err)
		}
		if latestReleaseAt.Valid {
			value := latestReleaseAt.Time
			item.LatestReleaseAt = &value
		}
		byTracker[item.TrackerID] = len(queue)
		queue = append(queue, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review queue trackers: %w", err)
	}
	if len(queue) == 0 {
		return queue, nil
	}

	suggestionRows, err := r.db.Query(`
		SELECT s.id, s.tracker_id, s.source_id, s.candidate_url, s.candidate_item_id,
		       s.candidate_title, s.candidate_cover_url, s.candidate_latest_chapter,
		       s.candidate_release_at, s.score, s.status, s.created_at
		FROM source_link_suggestions s
		INNER JOIN trackers t ON t.id = s.tracker_id
		WHERE s.source_id = ? AND s.status = 'pending' AND t.profile_id = ?
		ORDER BY s.tracker_id ASC, s.score DESC, s.id ASC
	`, sourceID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list pending suggestions: %w", err)
	}
	defer suggestionRows.Close()

	for suggestionRows.Next() {
		suggestion, err := scanLinkSuggestion(suggestionRows)
		if err != nil {
			return nil, err
		}
		if index, ok := byTracker[suggestion.TrackerID]; ok {
			queue[index].Suggestions = append(queue[index].Suggestions, suggestion)
		}
	}
	if err := suggestionRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending suggestions: %w", err)
	}

	return queue, nil
}

func scanLinkSuggestion(row rowScanner) (LinkSuggestion, error) {
	var suggestion LinkSuggestion
	var candidateItemID, candidateCoverURL sql.NullString
	var candidateLatestChapter sql.NullFloat64
	var candidateReleaseAt sql.NullTime
	if err := row.Scan(
		&suggestion.ID,
		&suggestion.TrackerID,
		&suggestion.SourceID,
		&suggestion.CandidateURL,
		&candidateItemID,
		&suggestion.CandidateTitle,
		&candidateCoverURL,
		&candidateLatestChapter,
		&candidateReleaseAt,
		&suggestion.Score,
		&suggestion.Status,
		&suggestion.CreatedAt,
	); err != nil {
		return suggestion, fmt.Errorf("scan link suggestion: %w", err)
	}
	if candidateItemID.Valid {
		suggestion.CandidateItemID = &candidateItemID.String
	}
	if candidateCoverURL.Valid {
		suggestion.CandidateCoverURL = &candidateCoverURL.String
	}
	if candidateLatestChapter.Valid {
		suggestion.CandidateLatestChapter = &candidateLatestChapter.Float64
	}
	if candidateReleaseAt.Valid {
		value := candidateReleaseAt.Time
		suggestion.CandidateReleaseAt = &value
	}
	return suggestion, nil
}

// GetPendingSuggestion loads one pending suggestion, verifying through the
// tracker that it belongs to the profile.
func (r *LinkSuggestionRepository) GetPendingSuggestion(profileID int64, suggestionID int64) (*LinkSuggestion, error) {
	row := r.db.QueryRow(`
		SELECT s.id, s.tracker_id, s.source_id, s.candidate_url, s.candidate_item_id,
		       s.candidate_title, s.candidate_cover_url, s.candidate_latest_chapter,
		       s.candidate_release_at, s.score, s.status, s.created_at
		FROM source_link_suggestions s
		INNER JOIN trackers t ON t.id = s.tracker_id
		WHERE s.id = ? AND s.status = 'pending' AND t.profile_id = ?
	`, suggestionID, profileID)

	suggestion, err := scanLinkSuggestion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &suggestion, nil
}

// DecideSuggestion moves one pending suggestion to accepted or rejected.
func (r *LinkSuggestionRepository) DecideSuggestion(profileID int64, suggestionID int64, status string) error {
	if status != LinkSuggestionAccepted && status != LinkSuggestionRejected {
		return fmt.Errorf("invalid suggestion decision %q", status)
	}
	_, err := r.db.Exec(`
		UPDATE source_link_suggestions
		SET status = ?, decided_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'pending'
		  AND EXISTS (SELECT 1 FROM trackers t WHERE t.id = tracker_id AND t.profile_id = ?)
	`, status, suggestionID, profileID)
	if err != nil {
		return fmt.Errorf("decide suggestion: %w", err)
	}
	return nil
}

// RejectPendingSiblings rejects a tracker's other pending candidates once one
// of them was accepted: a tracker links a source once.
func (r *LinkSuggestionRepository) RejectPendingSiblings(trackerID int64, sourceID int64, exceptID int64) error {
	_, err := r.db.Exec(`
		UPDATE source_link_suggestions
		SET status = 'rejected', decided_at = CURRENT_TIMESTAMP
		WHERE tracker_id = ? AND source_id = ? AND status = 'pending' AND id != ?
	`, trackerID, sourceID, exceptID)
	if err != nil {
		return fmt.Errorf("reject pending siblings: %w", err)
	}
	return nil
}

// DismissTracker records that the tracker has no match on the source: pending
// candidates are rejected and a marker row keeps it out of future scans.
func (r *LinkSuggestionRepository) DismissTracker(profileID int64, trackerID int64, sourceID int64) error {
	var owned int
	if err := r.db.QueryRow(`SELECT COUNT(1) FROM trackers WHERE id = ? AND profile_id = ?`, trackerID, profileID).Scan(&owned); err != nil {
		return fmt.Errorf("check tracker ownership: %w", err)
	}
	if owned == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin dismiss tracker tx: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE source_link_suggestions
		SET status = 'rejected', decided_at = CURRENT_TIMESTAMP
		WHERE tracker_id = ? AND source_id = ? AND status = 'pending'
	`, trackerID, sourceID); err != nil {
		tx.Rollback()
		return fmt.Errorf("reject pending on dismiss: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO source_link_suggestions (tracker_id, source_id, candidate_url, status, decided_at)
		VALUES (?, ?, '', 'dismissed', CURRENT_TIMESTAMP)
		ON CONFLICT(tracker_id, source_id, candidate_url)
		DO UPDATE SET status = 'dismissed', decided_at = CURRENT_TIMESTAMP
	`, trackerID, sourceID); err != nil {
		tx.Rollback()
		return fmt.Errorf("insert dismissal marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dismiss tracker tx: %w", err)
	}
	return nil
}
