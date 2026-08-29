package connectors

import (
	"fmt"
	"sync"
	"time"
)

// EscalatingBreaker is a connector-level circuit breaker for outages the
// shared per-host throttle cannot classify: a Cloudflare challenge or token
// rejection looks like any other failed response at the transport, but the
// connector knows it will not clear on its own schedule. Each cooldown that
// reopens without a successful request in between escalates: a site-wide
// challenge lasts hours to days, and retrying it every poll cycle re-confirms
// the same outage, so each relapse doubles the cooldown up to maxCooldown.
//
// Connectors expose it through the CooldownReporter interface so the poller
// skips a site known to be dark instead of burning a doomed request per
// tracker.
type EscalatingBreaker struct {
	// relapseWindow is how soon after a cooldown expires a new one must open
	// to count as the same outage. It has to outlast a full poll interval plus
	// a cycle's runtime, or the streak would reset between the cooldown
	// expiring and the poller's next attempt at the site.
	relapseWindow time.Duration
	// maxCooldown caps the escalation: long enough that a multi-day outage
	// costs a handful of probes per day, short enough that the connector
	// notices the site coming back within one sitting.
	maxCooldown time.Duration

	mu     sync.Mutex
	until  time.Time
	reason string
	// streak counts cooldowns that reopened without a successful request in
	// between, which is what escalates their duration.
	streak int
}

func NewEscalatingBreaker(relapseWindow time.Duration, maxCooldown time.Duration) *EscalatingBreaker {
	return &EscalatingBreaker{relapseWindow: relapseWindow, maxCooldown: maxCooldown}
}

// Remaining reports how much longer the breaker refuses requests and why;
// zero or negative means it is accepting requests. Its shape matches
// CooldownReporter so connectors can delegate to it directly.
func (b *EscalatingBreaker) Remaining() (time.Duration, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Until(b.until), b.reason
}

// Trip opens (or extends) the cooldown. base is the incident's own cooldown;
// the streak of unbroken relapses doubles it up to maxCooldown.
func (b *EscalatingBreaker) Trip(base time.Duration, reason string) {
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()

	switch {
	case now.Before(b.until):
		// A request that was already in flight when the circuit opened; the
		// same incident, not new evidence of the outage persisting.
	case !b.until.IsZero() && now.Before(b.until.Add(b.relapseWindow)):
		b.streak++
	default:
		b.streak = 0
	}

	duration := b.escalated(base)
	until := now.Add(duration)
	if until.After(b.until) {
		b.until = until
		b.reason = reason
		if b.streak > 0 {
			b.reason = fmt.Sprintf("%s (still failing after %d cooldowns)", reason, b.streak)
		}
	}
}

// escalated doubles base once per relapse, capped at maxCooldown. Callers hold b.mu.
func (b *EscalatingBreaker) escalated(base time.Duration) time.Duration {
	duration := base
	for i := 0; i < b.streak; i++ {
		duration *= 2
		if duration >= b.maxCooldown {
			return b.maxCooldown
		}
	}
	if duration > b.maxCooldown {
		return b.maxCooldown
	}
	return duration
}

// NoteSuccess closes the breaker: the site answered, so any expired cooldown
// record must not escalate the next one. Guarded against an active cooldown so
// a slow success that raced the circuit opening cannot clear it.
func (b *EscalatingBreaker) NoteSuccess() {
	now := time.Now().UTC()
	b.mu.Lock()
	if !b.until.After(now) {
		b.streak = 0
		b.until = time.Time{}
		b.reason = ""
	}
	b.mu.Unlock()
}
