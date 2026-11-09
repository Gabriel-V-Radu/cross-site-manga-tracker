package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	if cooling.QueueFull {
		t.Fatal("an open circuit is a real cooldown, not a full queue")
	}

	open := th.openCircuits(now.Add(time.Second))
	if len(open) != 1 || open[0].Host != "a.example" || open[0].BlockedFor != time.Minute-time.Second {
		t.Fatalf("openCircuits = %+v, want a.example blocked for 59s", open)
	}
	if open := th.openCircuits(now.Add(2 * time.Minute)); len(open) != 0 {
		t.Fatalf("an expired block is not an open circuit: %+v", open)
	}

	// The block expires on its own.
	if _, _, err := th.reserve("a.example", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("expired block should admit requests, got %v", err)
	}
}

// A host whose cooldown has just run out is on probation: one failed probe
// opens the circuit again, where it used to take a whole new run of failures —
// each of them a full timeout while the host stayed dark. A success ends the
// probation, and the host is back to the ordinary threshold.
func TestThrottlerReopensOnTheFirstFailureAfterACooldown(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)
	var cooling *SourceCoolingDownError

	for i := 0; i < 3; i++ {
		th.observe("a.example", now, true)
	}

	// A request that was in flight when the circuit opened fails afterwards:
	// the same incident, it must not move the cooldown.
	th.observe("a.example", now.Add(10*time.Second), true)
	if open := th.openCircuits(now.Add(10 * time.Second)); len(open) != 1 || open[0].BlockedFor != 50*time.Second {
		t.Fatalf("a late failure of the same incident moved the cooldown: %+v", open)
	}

	afterCooldown := now.Add(2 * time.Minute)
	if _, _, err := th.reserve("a.example", afterCooldown); err != nil {
		t.Fatalf("the probe after the cooldown must be admitted, got %v", err)
	}
	th.observe("a.example", afterCooldown, true)
	if _, _, err := th.reserve("a.example", afterCooldown.Add(time.Second)); !errors.As(err, &cooling) {
		t.Fatalf("one failed probe must reopen the circuit, got %v", err)
	}

	// The next probe succeeds: probation is over, one failure is a blip again.
	recovered := afterCooldown.Add(2 * time.Minute)
	th.observe("a.example", recovered, false)
	th.observe("a.example", recovered, true)
	if _, _, err := th.reserve("a.example", recovered.Add(time.Second)); err != nil {
		t.Fatalf("after a success a single failure must not open the circuit, got %v", err)
	}
}

// The connectors' shared transport gives up on a connection that will not open
// long before net/http's default thirty seconds.
func TestSharedTransportBoundsTheConnect(t *testing.T) {
	transport, ok := sharedTransport.(*http.Transport)
	if !ok {
		t.Fatalf("sharedTransport is %T, want *http.Transport", sharedTransport)
	}
	if transport == http.DefaultTransport {
		t.Fatal("the shared transport must be its own copy, not the process-wide default")
	}
	if transport.DialContext == nil {
		t.Fatal("the shared transport must dial through sharedDialer")
	}
	if sharedDialer.Timeout != connectTimeout || connectTimeout > 10*time.Second {
		t.Fatalf("connect timeout = %s (dialer %s), want at most 10s", connectTimeout, sharedDialer.Timeout)
	}
	if wrapped, ok := ThrottleTransport(nil).(*throttledTransport); !ok || wrapped.base != sharedTransport {
		t.Fatal("a throttled client with no transport of its own must use the shared one")
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
			// The host did nothing wrong; the refusal has to say so, or
			// anything classifying it reads a busy queue as an outage.
			if !cooling.QueueFull {
				t.Fatalf("cap refusal must be marked QueueFull: %v", cooling)
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

// A host that stops accepting connections must open the circuit even though
// Go's dial timeout error matches context.DeadlineExceeded; only the caller's
// own context ending is "the caller gave up".
func TestThrottledTransportCountsDialTimeoutsButNotCallerDeadlines(t *testing.T) {
	// A deadline already past makes the dialer give up before any syscall, so
	// this is Go's own dial-timeout error without depending on the network.
	_, dialErr := (&net.Dialer{Deadline: time.Now().Add(-time.Second)}).Dial("tcp", "127.0.0.1:9")
	if !errors.Is(dialErr, context.DeadlineExceeded) {
		t.Fatalf("precondition: a dial timeout should match context.DeadlineExceeded, got %v", dialErr)
	}

	roundTrips := func(th *throttler, ctx context.Context, n int, failure error) {
		transport := &throttledTransport{throttler: th, base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, failure
		})}
		for i := 0; i < n; i++ {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://a.example/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = transport.RoundTrip(req)
		}
	}

	dark := newTestThrottler()
	dark.gap = time.Millisecond
	roundTrips(dark, context.Background(), 3, fmt.Errorf("request failed: %w", dialErr))
	var cooling *SourceCoolingDownError
	if _, _, err := dark.reserve("a.example", time.Now()); !errors.As(err, &cooling) {
		t.Fatalf("three dial timeouts with the caller still waiting must open the circuit, got %v", err)
	}

	impatient := newTestThrottler()
	impatient.gap = time.Millisecond
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	roundTrips(impatient, expired, 3, context.DeadlineExceeded)
	if _, _, err := impatient.reserve("a.example", time.Now()); err != nil {
		t.Fatalf("a caller whose own deadline passed says nothing about the host: %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
