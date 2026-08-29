package connectors

import (
	"strings"
	"testing"
	"time"
)

// TestEscalatingBreakerBackoffCurve pins the backoff curve: each relapse
// doubles the base, capped at the configured maximum.
func TestEscalatingBreakerBackoffCurve(t *testing.T) {
	const maxCooldown = 6 * time.Hour
	cases := []struct {
		base   time.Duration
		streak int
		want   time.Duration
	}{
		{base: 30 * time.Minute, streak: 0, want: 30 * time.Minute},
		{base: 30 * time.Minute, streak: 1, want: time.Hour},
		{base: 30 * time.Minute, streak: 3, want: 4 * time.Hour},
		{base: 30 * time.Minute, streak: 4, want: maxCooldown},
		{base: 30 * time.Minute, streak: 50, want: maxCooldown},
		{base: 5 * time.Minute, streak: 2, want: 20 * time.Minute},
		{base: 12 * time.Hour, streak: 0, want: maxCooldown},
	}

	for _, tc := range cases {
		breaker := NewEscalatingBreaker(90*time.Minute, maxCooldown)
		breaker.streak = tc.streak
		if got := breaker.escalated(tc.base); got != tc.want {
			t.Fatalf("escalated(%s) with streak %d = %s, want %s", tc.base, tc.streak, got, tc.want)
		}
	}
}

// TestEscalatingBreakerEscalatesOnRelapse walks the streak state machine: a
// cooldown that reopens shortly after expiring doubles, a success in between
// resets the streak, and a relapse long after the window starts over at base.
func TestEscalatingBreakerEscalatesOnRelapse(t *testing.T) {
	const relapseWindow = 90 * time.Minute
	breaker := NewEscalatingBreaker(relapseWindow, 6*time.Hour)

	assertRemainingNear := func(t *testing.T, want time.Duration) {
		t.Helper()
		remaining, _ := breaker.Remaining()
		if remaining < want-time.Minute || remaining > want {
			t.Fatalf("expected roughly %s of cooldown, got %s", want, remaining)
		}
	}
	expireCooldown := func() {
		breaker.mu.Lock()
		breaker.until = time.Now().UTC().Add(-time.Minute)
		breaker.mu.Unlock()
	}

	breaker.Trip(30*time.Minute, "challenge")
	assertRemainingNear(t, 30*time.Minute)

	// A second failure while the cooldown is still open is the same incident.
	breaker.Trip(30*time.Minute, "challenge")
	assertRemainingNear(t, 30*time.Minute)

	// The cooldown expires, the next attempt fails again: escalate.
	expireCooldown()
	breaker.Trip(30*time.Minute, "challenge")
	assertRemainingNear(t, time.Hour)
	if _, reason := breaker.Remaining(); !strings.Contains(reason, "still failing") {
		t.Fatalf("expected the reason to name the escalation, got %q", reason)
	}

	expireCooldown()
	breaker.Trip(30*time.Minute, "challenge")
	assertRemainingNear(t, 2*time.Hour)

	// A successful request closes the breaker entirely.
	expireCooldown()
	breaker.NoteSuccess()
	breaker.Trip(30*time.Minute, "challenge")
	assertRemainingNear(t, 30*time.Minute)

	// A relapse far outside the window is a fresh outage, not a continuation.
	breaker.mu.Lock()
	breaker.until = time.Now().UTC().Add(-2 * relapseWindow)
	breaker.mu.Unlock()
	breaker.Trip(30*time.Minute, "challenge")
	assertRemainingNear(t, 30*time.Minute)
}

// TestEscalatingBreakerSuccessDuringCooldownDoesNotClear guards against a slow
// success that raced the circuit opening clearing an active cooldown.
func TestEscalatingBreakerSuccessDuringCooldownDoesNotClear(t *testing.T) {
	breaker := NewEscalatingBreaker(90*time.Minute, 6*time.Hour)
	breaker.Trip(30*time.Minute, "challenge")
	breaker.NoteSuccess()
	if remaining, _ := breaker.Remaining(); remaining <= 0 {
		t.Fatalf("an in-flight success must not clear an active cooldown, remaining = %s", remaining)
	}
}
