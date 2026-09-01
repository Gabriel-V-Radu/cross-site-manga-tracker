package connectors

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func newTestThrottler() *throttler {
	return &throttler{
		gap:              time.Second,
		failureThreshold: 3,
		cooldown:         time.Minute,
		hosts:            map[string]*hostState{},
	}
}

func TestThrottlerPacesRequestsPerHost(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)

	_, wait, err := th.reserve("a.example", now)
	if err != nil || wait != 0 {
		t.Fatalf("first request should pass immediately, got wait=%v err=%v", wait, err)
	}

	_, wait, err = th.reserve("a.example", now)
	if err != nil || wait != time.Second {
		t.Fatalf("second request should wait one gap, got wait=%v err=%v", wait, err)
	}

	// A different host has its own queue.
	_, wait, err = th.reserve("b.example", now)
	if err != nil || wait != 0 {
		t.Fatalf("other host should be unaffected, got wait=%v err=%v", wait, err)
	}

	// Once the gap has passed, no wait is imposed.
	_, wait, err = th.reserve("a.example", now.Add(5*time.Second))
	if err != nil || wait != 0 {
		t.Fatalf("request after the gap should pass immediately, got wait=%v err=%v", wait, err)
	}
}

func TestThrottlerOpensCircuitAfterConsecutiveFailures(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)

	for i := 0; i < 3; i++ {
		th.observe("a.example", now, true)
	}

	_, _, err := th.reserve("a.example", now.Add(time.Second))
	var cooling *SourceCoolingDownError
	if !errors.As(err, &cooling) {
		t.Fatalf("expected SourceCoolingDownError, got %v", err)
	}
	if cooling.Host != "a.example" {
		t.Fatalf("error names host %q", cooling.Host)
	}

	// The block expires on its own.
	if _, _, err := th.reserve("a.example", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("expired block should admit requests, got %v", err)
	}
}

func TestThrottlerSuccessResetsFailureStreak(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)

	th.observe("a.example", now, true)
	th.observe("a.example", now, true)
	th.observe("a.example", now, false)
	th.observe("a.example", now, true)
	th.observe("a.example", now, true)

	if _, _, err := th.reserve("a.example", now.Add(time.Hour)); err != nil {
		t.Fatalf("streak was broken by a success, circuit must stay closed: %v", err)
	}
}

func TestThrottlerHonorsHostGapOverride(t *testing.T) {
	th := newTestThrottler()
	th.gapOverrides = map[string]time.Duration{"slow.example": 5 * time.Second}
	now := time.Unix(1000, 0)

	if _, _, err := th.reserve("slow.example", now); err != nil {
		t.Fatalf("first request: %v", err)
	}
	_, wait, err := th.reserve("slow.example", now)
	if err != nil || wait != 5*time.Second {
		t.Fatalf("override gap = %v err=%v, want 5s", wait, err)
	}

	// Other hosts keep the default gap.
	if _, _, err := th.reserve("fast.example", now); err != nil {
		t.Fatalf("fast host first request: %v", err)
	}
	_, wait, err = th.reserve("fast.example", now)
	if err != nil || wait != time.Second {
		t.Fatalf("default gap = %v err=%v, want 1s", wait, err)
	}
}

func TestThrottlerCapsQueueWait(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)

	// Claim slots until the next one would exceed the cap.
	admitted := 0
	for i := 0; i < 100; i++ {
		_, _, err := th.reserve("a.example", now)
		if err != nil {
			var cooling *SourceCoolingDownError
			if !errors.As(err, &cooling) {
				t.Fatalf("cap refusal should be a SourceCoolingDownError, got %v", err)
			}
			break
		}
		admitted++
	}

	if admitted == 100 {
		t.Fatal("queue never hit the cap")
	}
	// A refusal must not have claimed a slot: the queue drains on schedule.
	_, wait, err := th.reserve("a.example", now.Add(time.Duration(admitted)*time.Second))
	if err != nil || wait != 0 {
		t.Fatalf("queue should have drained, got wait=%v err=%v", wait, err)
	}
}

// A caller that gives up before its slot arrives hands the slot back, so the
// host's queue does not keep pacing requests that never went out. Only the tail
// of the queue can be reclaimed: a slot with later requests already queued
// behind it stays claimed, since those requests were scheduled relative to it.
func TestThrottlerReleaseReclaimsTheLastSlotOnly(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)

	if _, _, err := th.reserve("a.example", now); err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, wait, err := th.reserve("a.example", now)
	if err != nil || wait != time.Second {
		t.Fatalf("second request wait=%v err=%v, want 1s", wait, err)
	}

	// The second caller gives up: the next request takes its slot.
	th.release("a.example", second)
	third, wait, err := th.reserve("a.example", now)
	if err != nil || wait != time.Second {
		t.Fatalf("after release wait=%v err=%v, want 1s (the reclaimed slot)", wait, err)
	}
	if !third.Equal(second) {
		t.Fatalf("reclaimed slot = %v, want %v", third, second)
	}

	// A slot with a later one queued behind it is not reclaimed.
	if _, _, err := th.reserve("a.example", now); err != nil {
		t.Fatalf("fourth request: %v", err)
	}
	th.release("a.example", third)
	_, wait, err = th.reserve("a.example", now)
	if err != nil || wait != 3*time.Second {
		t.Fatalf("release of a mid-queue slot must not move the tail: wait=%v err=%v, want 3s", wait, err)
	}
}

// The transport releases the slot when the request's context is cancelled
// during the pacing wait, so an abandoned burst leaves no phantom pacing.
func TestThrottledTransportReleasesSlotOnCancelledWait(t *testing.T) {
	th := newTestThrottler()
	transport := &throttledTransport{throttler: th, base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("a cancelled request must not reach the network")
		return nil, nil
	})}

	// Claim the immediate slot so the request under test has to wait.
	if _, _, err := th.reserve("a.example", time.Now()); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://a.example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip error = %v, want context.Canceled", err)
	}

	// The slot the cancelled request held is free again: the next request
	// waits one gap from the seed, not two.
	_, wait, err := th.reserve("a.example", time.Now())
	if err != nil {
		t.Fatalf("follow-up request: %v", err)
	}
	if wait > time.Second {
		t.Fatalf("follow-up wait = %v, want at most one gap: the cancelled slot was not released", wait)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
