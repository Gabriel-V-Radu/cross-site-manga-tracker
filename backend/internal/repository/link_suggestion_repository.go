package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/searchutil"
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

// LinkScanFilter narrows which of a profile's unlinked trackers a scan (and
// the review queue) covers. The zero value filters nothing. The point is
// workload: most of the time only a slice needs linking — series whose
// primary source is down, or series with no working fallback yet — and a
// scan of everything wastes requests on trackers that are already fine.
type LinkScanFilter struct {
	// Statuses keeps only trackers in these reading statuses.
	Statuses []string
	// PrimarySourceIDs keeps only trackers whose primary source is one of
	// these. A non-nil empty slice matches nothing (the caller resolved
	// "broken sources" and found none).
	PrimarySourceIDs []int64
	// MaxAlternates keeps only trackers with at most this many linked
	// alternate sources (linked sources other than the primary). Nil = any.
	MaxAlternates *int
}

// linkableTrackersQuery selects a profile's trackers that still lack the given
// source: not their primary, not already linked, not dismissed, no accepted
// suggestion — narrowed by the filter.
func linkableTrackersQuery(profileID int64, sourceID int64, filter LinkScanFilter) (string, []any) {
	query := `
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
	args := []any{sourceID, sourceID, profileID, sourceID}

	if len(filter.Statuses) > 0 {
		query += ` AND LOWER(TRIM(t.status)) IN (` + sqlPlaceholders(len(filter.Statuses)) + `)`
		for _, status := range filter.Statuses {
			args = append(args, strings.ToLower(strings.TrimSpace(status)))
		}
	}

	if filter.PrimarySourceIDs != nil {
		if len(filter.PrimarySourceIDs) == 0 {
			// "Broken sources" resolved to none: match nothing rather than
			// silently widening to everything.
			query += ` AND 1 = 0`
		} else {
			query += ` AND t.source_id IN (` + sqlPlaceholders(len(filter.PrimarySourceIDs)) + `)`
			for _, id := range filter.PrimarySourceIDs {
				args = append(args, id)
			}
		}
	}

	if filter.MaxAlternates != nil {
		query += ` AND (
			SELECT COUNT(1) FROM tracker_sources alt
			WHERE alt.tracker_id = t.id AND alt.source_id != t.source_id
		) <= ?`
		args = append(args, *filter.MaxAlternates)
	}

	return query, args
}

// ListScanTargets returns the trackers a scan of sourceID should look up.
func (r *LinkSuggestionRepository) ListScanTargets(profileID int64, sourceID int64, filter LinkScanFilter) ([]LinkScanTarget, error) {
	query, args := linkableTrackersQuery(profileID, sourceID, filter)
	rows, err := r.db.Query(query+` ORDER BY t.title ASC`, args...)
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
func (r *LinkSuggestionRepository) ListReviewQueue(profileID int64, sourceID int64, filter LinkScanFilter) ([]LinkReviewTracker, error) {
	query, args := linkableTrackersQuery(profileID, sourceID, filter)
	rows, err := r.db.Query(query+` ORDER BY t.title ASC`, args...)
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

// pendingSuggestionByIDSQL loads one pending candidate by id, verifying
// through the tracker that it belongs to the profile.
const pendingSuggestionByIDSQL = `
	SELECT s.id, s.tracker_id, s.source_id, s.candidate_url, s.candidate_item_id,
	       s.candidate_title, s.candidate_cover_url, s.candidate_latest_chapter,
	       s.candidate_release_at, s.score, s.status, s.created_at
	FROM source_link_suggestions s
	INNER JOIN trackers t ON t.id = s.tracker_id
	WHERE s.id = ? AND s.status = 'pending' AND t.profile_id = ?
`

// GetPendingSuggestion loads one pending suggestion, verifying through the
// tracker that it belongs to the profile.
func (r *LinkSuggestionRepository) GetPendingSuggestion(profileID int64, suggestionID int64) (*LinkSuggestion, error) {
	suggestion, err := scanLinkSuggestion(r.db.QueryRow(pendingSuggestionByIDSQL, suggestionID, profileID))
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
	if _, err := r.db.Exec(rejectPendingSuggestionsSQL, trackerID, sourceID, exceptID); err != nil {
		return fmt.Errorf("reject pending siblings: %w", err)
	}
	return nil
}

// AcceptSuggestion applies a whole accept decision: the tracker's link to the
// source, the accepted status, and the rejection of that tracker's other
// pending candidates on the same source. It returns the accepted suggestion,
// or nil when the id is not (or is no longer) a pending candidate of the
// profile.
//
// The three writes are one transaction because every partial outcome is a
// contradiction the Link Review cannot recover from on its own: a suggestion
// marked accepted with no link row, or a link whose siblings stayed pending
// and which the queue therefore keeps offering.
//
// With MaxOpenConns(1) the transaction holds the pool's only connection, so
// every statement below has to go through tx. A repository call that reaches
// for r.db while this tx is open waits forever for the connection only this tx
// can release.
func (r *LinkSuggestionRepository) AcceptSuggestion(profileID int64, suggestionID int64) (*LinkSuggestion, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin accept suggestion tx: %w", err)
	}
	defer tx.Rollback()

	suggestion, err := scanLinkSuggestion(tx.QueryRow(pendingSuggestionByIDSQL, suggestionID, profileID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	if err := upsertTrackerSourceTx(tx, suggestion.TrackerID, models.TrackerSource{
		SourceID:     suggestion.SourceID,
		SourceItemID: suggestion.CandidateItemID,
		SourceURL:    suggestion.CandidateURL,
	}); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		UPDATE source_link_suggestions
		SET status = 'accepted', decided_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, suggestion.ID); err != nil {
		return nil, fmt.Errorf("mark suggestion accepted: %w", err)
	}
	if err := rejectPendingSuggestionsTx(tx, suggestion.TrackerID, suggestion.SourceID, suggestion.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit accept suggestion tx: %w", err)
	}

	suggestion.Status = LinkSuggestionAccepted
	return &suggestion, nil
}

// ApplyManualLink stores a hand-pasted link and settles the tracker's review
// on the source the queue is showing. The pasted URL often belongs to another
// site — that is the point of the fallback — and then the tracker is marked
// dismissed for the reviewed source so it leaves the queue; when it is the
// reviewed source, the candidates the user bypassed are rejected instead.
//
// One transaction for the same reason as AcceptSuggestion: a link stored
// without its review settled leaves the tracker in the queue offering
// candidates for a series that is already linked. Every statement goes through
// tx — see the connection note there.
func (r *LinkSuggestionRepository) ApplyManualLink(profileID int64, trackerID int64, link models.TrackerSource, reviewSourceID int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin manual link tx: %w", err)
	}
	defer tx.Rollback()

	owned, err := trackerOwnedByProfileTx(tx, trackerID, profileID)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}

	if err := upsertTrackerSourceTx(tx, trackerID, link); err != nil {
		return err
	}
	if err := rejectPendingSuggestionsTx(tx, trackerID, reviewSourceID, 0); err != nil {
		return err
	}
	if link.SourceID != reviewSourceID {
		if err := markTrackerDismissedTx(tx, trackerID, reviewSourceID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit manual link tx: %w", err)
	}
	return nil
}

const rejectPendingSuggestionsSQL = `
	UPDATE source_link_suggestions
	SET status = 'rejected', decided_at = CURRENT_TIMESTAMP
	WHERE tracker_id = ? AND source_id = ? AND status = 'pending' AND id != ?
`

// rejectPendingSuggestionsTx rejects a tracker's pending candidates on one
// source. exceptID spares the candidate that was just accepted; 0 spares none.
func rejectPendingSuggestionsTx(tx *sql.Tx, trackerID int64, sourceID int64, exceptID int64) error {
	if _, err := tx.Exec(rejectPendingSuggestionsSQL, trackerID, sourceID, exceptID); err != nil {
		return fmt.Errorf("reject pending suggestions: %w", err)
	}
	return nil
}

// markTrackerDismissedTx writes the marker row that keeps a reviewed tracker
// out of that source's future scans.
func markTrackerDismissedTx(tx *sql.Tx, trackerID int64, sourceID int64) error {
	if _, err := tx.Exec(`
		INSERT INTO source_link_suggestions (tracker_id, source_id, candidate_url, status, decided_at)
		VALUES (?, ?, '', 'dismissed', CURRENT_TIMESTAMP)
		ON CONFLICT(tracker_id, source_id, candidate_url)
		DO UPDATE SET status = 'dismissed', decided_at = CURRENT_TIMESTAMP
	`, trackerID, sourceID); err != nil {
		return fmt.Errorf("insert dismissal marker: %w", err)
	}
	return nil
}

func trackerOwnedByProfileTx(tx *sql.Tx, trackerID int64, profileID int64) (bool, error) {
	var owned int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM trackers WHERE id = ? AND profile_id = ?`, trackerID, profileID).Scan(&owned); err != nil {
		return false, fmt.Errorf("check tracker ownership: %w", err)
	}
	return owned > 0, nil
}

// upsertTrackerSourceTx links a tracker to a source through the transaction
// handle. TrackerRepository.UpsertTrackerSource runs the same statement, but
// on the *sql.DB: with MaxOpenConns(1), calling it while a transaction is open
// would wait forever for the connection that transaction holds.
func upsertTrackerSourceTx(tx *sql.Tx, trackerID int64, source models.TrackerSource) error {
	sourceURL := strings.TrimSpace(source.SourceURL)
	if source.SourceID <= 0 || sourceURL == "" {
		return fmt.Errorf("link tracker source: a source and a url are required")
	}
	if _, err := tx.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tracker_id, source_id, source_url)
		DO UPDATE SET
			source_item_id = excluded.source_item_id,
			updated_at = CURRENT_TIMESTAMP
	`, trackerID, source.SourceID, source.SourceItemID, sourceURL); err != nil {
		return fmt.Errorf("upsert tracker source: %w", err)
	}
	return nil
}

// MergeRelatedTitles unions new alternate titles into a tracker's stored set.
// The link scan calls it when a metadata aggregator confirms a series' other
// names: those names make every future scan and dashboard search match better,
// so they are worth keeping once learned.
func (r *LinkSuggestionRepository) MergeRelatedTitles(trackerID int64, titles []string) error {
	incoming := searchutil.FilterEnglishAlphabetNames(titles)
	if len(incoming) == 0 {
		return nil
	}

	// Read-merge-write in one transaction: a concurrent writer (a scan on
	// another goroutine, a dashboard edit) slipping between the read and the
	// write would have its titles overwritten by a merge that never saw them.
	// With MaxOpenConns(1) the tx holds the pool's only connection, so both
	// statements must use tx, never r.db.
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin merge related titles tx: %w", err)
	}
	defer tx.Rollback()

	var existingRaw sql.NullString
	if err := tx.QueryRow(`SELECT related_titles FROM trackers WHERE id = ?`, trackerID).Scan(&existingRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read related titles: %w", err)
	}

	existing := []string{}
	if existingRaw.Valid {
		existing = decodeRelatedTitlesJSON(existingRaw.String)
	}
	merged := sanitizeRelatedTitles(append(existing, incoming...))
	if len(merged) == len(sanitizeRelatedTitles(existing)) {
		return nil
	}

	encoded := encodeRelatedTitlesJSON(merged)
	if _, err := tx.Exec(`
		UPDATE trackers SET related_titles = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, encoded, trackerID); err != nil {
		return fmt.Errorf("write related titles: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit merge related titles tx: %w", err)
	}
	return nil
}

// DismissTracker records that the tracker has no match on the source: pending
// candidates are rejected and a marker row keeps it out of future scans.
func (r *LinkSuggestionRepository) DismissTracker(profileID int64, trackerID int64, sourceID int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin dismiss tracker tx: %w", err)
	}
	defer tx.Rollback()

	// Ownership is read inside the transaction: a check on r.db beforehand
	// would be a separate snapshot, and with MaxOpenConns(1) it could not run
	// at all once the transaction below holds the connection.
	owned, err := trackerOwnedByProfileTx(tx, trackerID, profileID)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}

	if err := rejectPendingSuggestionsTx(tx, trackerID, sourceID, 0); err != nil {
		return err
	}
	if err := markTrackerDismissedTx(tx, trackerID, sourceID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dismiss tracker tx: %w", err)
	}
	return nil
}
