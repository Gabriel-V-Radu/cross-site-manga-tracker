package scheduler

import (
	"sort"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// The poller no longer asks every tracker's primary source on every cycle. A
// tracker linked to several sites takes turns: each cycle asks the site in its
// rotation that it asked longest ago, so the cycle's requests spread across the
// sites instead of piling onto whichever one most trackers were created on
// (MangaFire, behind a request signer and recurring Cloudflare challenges, was
// the primary of most of the library and carried nearly all of its polls).
//
// The bound that makes this safe: a tracker's rotation holds at most
// maxRotationSources sites and every member is asked at least once per that
// many cycles, so a chapter that only one of them carries yet is found within
// that many cycles. Usually sooner — the sites in a rotation pick a chapter up
// within the same hour, and whichever is asked first reports it.
//
// Each poll still makes one request in the common case. Only when the site
// whose turn it is fails, answers without a usable number, or claims a new
// chapter it has not earned the trust to report alone (see pollCandidate.
// trusted) does the poll walk on to the next site, then to the sites that
// never take a turn, as the old primary-then-alternates fallback did.

// maxRotationSources caps how many of a tracker's linked sites take turns.
// Three is the detection bound asked for: a new chapter carried by any one
// member is found within three cycles.
const maxRotationSources = 3

// pollCandidate is one linked site in a poll's plan.
type pollCandidate struct {
	ref       repository.TrackerSourceRef
	connector connectors.Connector
	primary   bool
	// trusted says the site's report may advance the stored chapter on its
	// own: the primary always, an alternate only while its last answer
	// carried the chapter then stored (tracker_sources.in_step). A site that
	// numbers chapters its own way would otherwise push the tracker ahead on
	// its first turn and, agreeing with itself on every later one, keep it
	// there.
	trusted bool
}

// pollPlan is the order one poll asks a tracker's linked sites in: the
// rotation members, the one asked longest ago first, then the sites that
// never take a turn — trailing feeds and members past the cap — as
// last-resort fallbacks.
type pollPlan struct {
	candidates []pollCandidate
}

// shardKey is the source this poll's first request should go to, which is
// the shard the cycle runs it on so each host keeps its own sequential queue.
// A site that is cooling down is skipped without a request, so the poll's
// first real request goes to the site after it.
func (plan pollPlan) shardKey() string {
	for _, candidate := range plan.candidates {
		if !coolingDown(candidate.connector) {
			return candidate.ref.SourceKey
		}
	}
	if len(plan.candidates) == 0 {
		return ""
	}
	return plan.candidates[0].ref.SourceKey
}

func coolingDown(connector connectors.Connector) bool {
	reporter, ok := connector.(connectors.CooldownReporter)
	if !ok {
		return false
	}
	remaining, _ := reporter.CooldownRemaining()
	return remaining > 0
}

func trailingFeed(connector connectors.Connector) bool {
	info, ok := connector.(connectors.PollRotationInfo)
	return ok && info.TrailingChapterFeed()
}

// planPoll orders a tracker's linked sites for this cycle's poll. It reports
// false when the primary has no connector: such a tracker is reported on the
// health board for the reader to relink rather than polled.
func (p *Poller) planPoll(tracker repository.PollingTracker) (pollPlan, bool) {
	primaryConnector, ok := p.registry.Get(tracker.SourceKey)
	if !ok {
		return pollPlan{}, false
	}

	rotation := []pollCandidate{{
		ref: repository.TrackerSourceRef{
			SourceID:     tracker.SourceID,
			SourceKey:    tracker.SourceKey,
			SourceItemID: tracker.SourceItemID,
			SourceURL:    tracker.SourceURL,
			LastPolledAt: tracker.PrimaryLastPolledAt,
		},
		connector: primaryConnector,
		primary:   true,
		trusted:   true,
	}}

	var eligible, fallbackOnly []pollCandidate
	for _, ref := range tracker.AlternateSources {
		if strings.TrimSpace(ref.SourceURL) == "" {
			continue
		}
		connector, ok := p.registry.Get(ref.SourceKey)
		if !ok {
			continue
		}
		candidate := pollCandidate{ref: ref, connector: connector, trusted: ref.InStep}
		if trailingFeed(connector) {
			fallbackOnly = append(fallbackOnly, candidate)
			continue
		}
		eligible = append(eligible, candidate)
	}

	// Past the cap, sites in step win the seats, then the alternates' own
	// order (by source name) decides; the rest still answer as fallbacks.
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].trusted && !eligible[j].trusted })
	for _, candidate := range eligible {
		if len(rotation) < maxRotationSources {
			rotation = append(rotation, candidate)
			continue
		}
		fallbackOnly = append(fallbackOnly, candidate)
	}

	orderRotation(rotation, tracker.ID)
	return pollPlan{candidates: append(rotation, fallbackOnly...)}, true
}

// orderRotation puts the member asked longest ago first. Members never asked
// share the zero time and come first of all; between equal times the order is
// the rotation's own, started at an offset taken from the tracker id. Without
// that offset every tracker would begin on its primary and the library's
// first three cycles would hit one site each — and stay that way, because
// last-polled order keeps whatever phase the trackers start in. With it, a
// cycle spreads evenly from the first one. Tracker ids start at 1, so tracker
// 1 starts on its primary.
func orderRotation(rotation []pollCandidate, trackerID int64) {
	count := len(rotation)
	if count < 2 {
		return
	}
	offset := int(uint64(trackerID-1) % uint64(count))
	type ordered struct {
		candidate pollCandidate
		polledAt  time.Time
		tieRank   int
	}
	entries := make([]ordered, count)
	for index, candidate := range rotation {
		var polledAt time.Time
		if candidate.ref.LastPolledAt != nil {
			polledAt = *candidate.ref.LastPolledAt
		}
		entries[index] = ordered{candidate: candidate, polledAt: polledAt, tieRank: (index - offset + count) % count}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].polledAt.Equal(entries[j].polledAt) {
			return entries[i].polledAt.Before(entries[j].polledAt)
		}
		return entries[i].tieRank < entries[j].tieRank
	})
	for index := range entries {
		rotation[index] = entries[index].candidate
	}
}
