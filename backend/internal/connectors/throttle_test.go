package connectors

import (
	"errors"
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

	wait, err := th.reserve("a.example", now)
	if err != nil || wait != 0 {
		t.Fatalf("first request should pass immediately, got wait=%v err=%v", wait, err)
	}

	wait, err = th.reserve("a.example", now)
	if err != nil || wait != time.Second {
		t.Fatalf("second request should wait one gap, got wait=%v err=%v", wait, err)
	}

	// A different host has its own queue.
	wait, err = th.reserve("b.example", now)
	if err != nil || wait != 0 {
		t.Fatalf("other host should be unaffected, got wait=%v err=%v", wait, err)
	}

	// Once the gap has passed, no wait is imposed.
	wait, err = th.reserve("a.example", now.Add(5*time.Second))
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

	_, err := th.reserve("a.example", now.Add(time.Second))
	var cooling *SourceCoolingDownError
	if !errors.As(err, &cooling) {
		t.Fatalf("expected SourceCoolingDownError, got %v", err)
	}
	if cooling.Host != "a.example" {
		t.Fatalf("error names host %q", cooling.Host)
	}

	// The block expires on its own.
	if _, err := th.reserve("a.example", now.Add(2*time.Minute)); err != nil {
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

	if _, err := th.reserve("a.example", now.Add(time.Hour)); err != nil {
		t.Fatalf("streak was broken by a success, circuit must stay closed: %v", err)
	}
}

func TestThrottlerCapsQueueWait(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)

	// Claim slots until the next one would exceed the cap.
	admitted := 0
	for i := 0; i < 100; i++ {
		_, err := th.reserve("a.example", now)
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
	wait, err := th.reserve("a.example", now.Add(time.Duration(admitted)*time.Second))
	if err != nil || wait != 0 {
		t.Fatalf("queue should have drained, got wait=%v err=%v", wait, err)
	}
}
