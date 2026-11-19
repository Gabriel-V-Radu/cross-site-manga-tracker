package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

// sendThrough makes one request through a throttled transport whose network
// always answers with failure, on the caller's context.
func sendThrough(t *testing.T, th *throttler, ctx context.Context, failure error) error {
	t.Helper()
	transport := &throttledTransport{throttler: th, base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, failure
	})}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://a.example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req)
	return err
}

// unpacedThrottler is a test throttler whose requests never wait for a slot,
// so every one of them reaches the network and what is under test is how its
// outcome is recorded.
func unpacedThrottler() *throttler {
	th := newTestThrottler()
	th.gap = time.Nanosecond
	return th
}

// A caller that gives up says nothing about the host — not that it failed, and
// not that it answered. It used to be recorded as a success, which wiped the
// failure streak: a host hanging every request until its callers' deadlines
// passed never reached the threshold, however many real failures came between
// (api.comick.dev on the Pi, 2026-11-16).
func TestThrottledTransportAbandonedRequestDoesNotClearTheFailureStreak(t *testing.T) {
	th := unpacedThrottler()
	down := errors.New("connection reset by peer")
	gaveUp, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_ = sendThrough(t, th, context.Background(), down)
	_ = sendThrough(t, th, context.Background(), down)
	_ = sendThrough(t, th, gaveUp, context.DeadlineExceeded)
	_ = sendThrough(t, th, context.Background(), down)

	var cooling *SourceCoolingDownError
	if _, _, err := th.reserve("a.example", time.Now()); !errors.As(err, &cooling) {
		t.Fatalf("three failures with an abandoned request between them must open the circuit, got %v", err)
	}
}

// The same for a probation: the first request after a cooldown being abandoned
// is not the host showing it is back, so the next failure still reopens the
// circuit at once.
func TestThrottledTransportAbandonedProbeDoesNotEndTheProbation(t *testing.T) {
	th := unpacedThrottler()
	th.cooldown = 150 * time.Millisecond
	down := errors.New("connection reset by peer")
	gaveUp, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for i := 0; i < 3; i++ {
		_ = sendThrough(t, th, context.Background(), down)
	}
	time.Sleep(200 * time.Millisecond)

	if err := sendThrough(t, th, gaveUp, context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the abandoned probe must have gone out and come back as the caller's deadline, got %v", err)
	}
	// The abandoned probe gave up its claim, so this one is admitted as the
	// next probe — and, failing, reopens the circuit on its own.
	if err := sendThrough(t, th, context.Background(), down); !errors.Is(err, down) {
		t.Fatalf("the next request must be let through as the probe, got %v", err)
	}
	var cooling *SourceCoolingDownError
	if _, _, err := th.reserve("a.example", time.Now()); !errors.As(err, &cooling) || cooling.Probing {
		t.Fatalf("one failure on probation must reopen the circuit, got %v", err)
	}
}

func TestJudgeOutcome(t *testing.T) {
	live := context.Background()
	gaveUp, cancel := context.WithCancel(context.Background())
	cancel()
	status := func(code int) *http.Response { return &http.Response{StatusCode: code} }

	cases := []struct {
		name string
		ctx  context.Context
		res  *http.Response
		err  error
		want requestOutcome
	}{
		{"an answer", live, status(200), nil, outcomeAnswered},
		{"a 404 is an answer", live, status(404), nil, outcomeAnswered},
		{"a refusal", live, status(403), nil, outcomeFailed},
		{"a rate limit", live, status(429), nil, outcomeFailed},
		{"a server error", live, status(503), nil, outcomeFailed},
		{"unreachable", live, nil, errors.New("no route to host"), outcomeFailed},
		{"the transport's own timeout, caller still waiting", live, nil, fmt.Errorf("timeout awaiting response headers: %w", context.DeadlineExceeded), outcomeFailed},
		{"the caller gave up", gaveUp, nil, errors.New("no route to host"), outcomeInconclusive},
		{"cancelled from above", live, nil, fmt.Errorf("request: %w", context.Canceled), outcomeInconclusive},
		{"an answer that arrived as the caller left is still an answer", gaveUp, status(200), nil, outcomeAnswered},
	}
	for _, testCase := range cases {
		if got := judgeOutcome(testCase.ctx, testCase.res, testCase.err); got != testCase.want {
			t.Errorf("%s: outcome %d, want %d", testCase.name, got, testCase.want)
		}
	}
}

// A host that accepts a request and then says nothing must fail on the
// transport's clock, not the caller's: only then is the failure the host's,
// and only then does the circuit open. Left to the caller's deadline the same
// hang is "the caller gave up", every time.
func TestResponseHeaderTimeoutMakesAHungHostAFailure(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)

	get := func(th *throttler, base *http.Transport, budget time.Duration) {
		defer base.CloseIdleConnections()
		client := &http.Client{Transport: &throttledTransport{base: base, throttler: th}}
		for i := 0; i < 3; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res, err := client.Do(req); err == nil {
				res.Body.Close()
				t.Fatal("the hung server must not answer")
			}
			cancel()
		}
	}

	var cooling *SourceCoolingDownError
	bounded := unpacedThrottler()
	get(bounded, &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}, 10*time.Second)
	if _, _, err := bounded.reserve("127.0.0.1", time.Now()); !errors.As(err, &cooling) {
		t.Fatalf("three header timeouts with the caller still waiting must open the circuit, got %v", err)
	}

	unbounded := unpacedThrottler()
	get(unbounded, &http.Transport{}, 50*time.Millisecond)
	if _, _, err := unbounded.reserve("127.0.0.1", time.Now()); err != nil {
		t.Fatalf("precondition: with only the caller's deadline the same hang never opens the circuit, got %v", err)
	}

	transport, ok := sharedTransport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != ResponseHeaderTimeout {
		t.Fatalf("the shared transport must carry ResponseHeaderTimeout, got %T %v", sharedTransport, transport)
	}
	if ResponseHeaderTimeout <= 0 || ResponseHeaderTimeout >= 30*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s: it has to pass before a cover download's 30s budget does", ResponseHeaderTimeout)
	}
}

// When a cooldown runs out, one request finds out whether the host is back and
// the rest are refused until it has. They used to be let through together, and
// against a host still dark each spent its own full timeout.
func TestThrottlerLetsOneProbeThroughAfterACooldown(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		th.observe("a.example", now, true)
	}
	after := now.Add(2 * time.Minute)
	var cooling *SourceCoolingDownError
	refusedAsProbing := func(label string, at time.Time) {
		t.Helper()
		if _, _, err := th.reserve("a.example", at); !errors.As(err, &cooling) || !cooling.Probing || cooling.QueueFull {
			t.Fatalf("%s: want a refusal marked Probing, got %v", label, err)
		}
	}
	admitted := func(label string, at time.Time) time.Time {
		t.Helper()
		slot, _, err := th.reserve("a.example", at)
		if err != nil {
			t.Fatalf("%s: want the request admitted, got %v", label, err)
		}
		return slot
	}

	probe := admitted("the first request after the cooldown", after)
	refusedAsProbing("a second request while the probe is out", after)
	if _, _, err := th.reserve("b.example", after); err != nil {
		t.Fatalf("another host is not on probation: %v", err)
	}

	// The probe's caller gives up after it went out: no verdict, and the next
	// request takes over as the probe.
	th.endProbe("a.example", probe)
	second := admitted("the request after an abandoned probe", after.Add(time.Second))
	refusedAsProbing("a request beside the second probe", after.Add(time.Second))

	// That one's caller gives up while still waiting for its slot.
	th.release("a.example", second)
	third := admitted("the request after a probe that never went out", after.Add(2*time.Second))

	// A claim nothing ever cleared lapses instead of refusing the host for good.
	refusedAsProbing("just inside the claim's limit", third.Add(probeClaimLimit-time.Second))
	admitted("past the claim's limit", third.Add(probeClaimLimit+time.Second))

	// The host answers: the probation is over and requests queue as usual.
	recovered := third.Add(2 * probeClaimLimit)
	th.observe("a.example", recovered, false)
	admitted("after a success", recovered)
	admitted("and the one behind it", recovered)
}

// A request that waited for its slot checks the circuit again before it goes
// out: the requests ahead of it in the queue may have opened it in the
// meantime, and the cooldown is for the queued requests as much as for the
// ones that had not arrived yet.
func TestThrottledTransportDoesNotSendARequestTheCircuitOpenedAhead(t *testing.T) {
	th := newTestThrottler()
	th.gap = 80 * time.Millisecond
	transport := &throttledTransport{throttler: th, base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Error("a request queued behind an opened circuit must not reach the network")
		return nil, errors.New("unreachable")
	})}

	// Claim the immediate slot so the request under test has to wait.
	if _, _, err := th.reserve("a.example", time.Now()); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://a.example/x", nil)
		if err != nil {
			result <- err
			return
		}
		_, err = transport.RoundTrip(req)
		result <- err
	}()
	// Let it take its place in the queue, then open the circuit under it.
	time.Sleep(20 * time.Millisecond)
	th.block("a.example", time.Now())

	var cooling *SourceCoolingDownError
	select {
	case err := <-result:
		if !errors.As(err, &cooling) || cooling.QueueFull || cooling.Probing {
			t.Fatalf("want the cooldown's own refusal, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued request never returned")
	}
}

// The refusal hands the slot back, like a caller who gave up waiting.
func TestThrottlerClearToSendReleasesARefusedSlot(t *testing.T) {
	th := newTestThrottler()
	now := time.Unix(1000, 0)
	if _, _, err := th.reserve("a.example", now); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	queued, wait, err := th.reserve("a.example", now)
	if err != nil || wait != time.Second {
		t.Fatalf("queued request wait=%v err=%v, want 1s", wait, err)
	}
	if err := th.clearToSend("a.example", queued, queued); err != nil {
		t.Fatalf("with the circuit closed the request goes out: %v", err)
	}

	th.block("a.example", now)
	var cooling *SourceCoolingDownError
	if err := th.clearToSend("a.example", queued, queued); !errors.As(err, &cooling) {
		t.Fatalf("want a cooldown refusal, got %v", err)
	}
	if got := th.hosts["a.example"].nextAllowed; !got.Equal(queued) {
		t.Fatalf("the refused slot was not handed back: next slot %v, want %v", got, queued)
	}
}

// Every table is keyed by host in lower case; a URL keeps the casing it was
// written with.
func TestThrottleKeysHostsInLowerCase(t *testing.T) {
	th := newTestThrottler()
	th.gapOverrides = map[string]time.Duration{"slow.example": 5 * time.Second}
	transport := &throttledTransport{throttler: th, base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://SLOW.Example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if _, ok := th.hosts["SLOW.Example"]; ok {
		t.Fatal("the host must not be tracked under the URL's own casing")
	}
	if _, wait, err := th.reserve("slow.example", time.Now()); err != nil || wait < 4*time.Second {
		t.Fatalf("the next request to the host must wait out its override gap, got wait=%v err=%v", wait, err)
	}

	NoteHostRateLimited(" Rate-Limited.Throttle-Test.EXAMPLE ")
	found := false
	for _, circuit := range OpenHostCircuits(time.Now()) {
		found = found || circuit.Host == "rate-limited.throttle-test.example"
	}
	if !found {
		t.Fatalf("a rate limit must open the circuit under the lower-cased host: %+v", OpenHostCircuits(time.Now()))
	}
}

// CooldownOf is the one answer to "is this site being left alone": the
// connector's own breaker, or the shared circuit of any host it claims.
func TestCooldownOfSeesTheSharedCircuitOfAClaimedHost(t *testing.T) {
	// The circuits opened here are the process's own and stay open for ten
	// minutes, so the hosts are this run's alone (-count repeats the test).
	siteHost := fmt.Sprintf("cooldown-of-%d.example", time.Now().UnixNano())
	otherHost := "elsewhere-" + siteHost
	site := &coolingTestConnector{Site: Site{SiteKey: "cooldownof", SiteName: "CooldownOf", SiteHosts: []string{siteHost}}}
	other := &coolingTestConnector{Site: Site{SiteKey: "elsewhere", SiteName: "Elsewhere", SiteHosts: []string{otherHost}}}
	if left, _ := CooldownOf(site, time.Now()); left != 0 {
		t.Fatalf("precondition: nothing is cooling down yet, got %s", left)
	}

	NoteHostRateLimited("api." + siteHost)
	left, reason := CooldownOf(site, time.Now())
	if left <= 0 || left > hostCooldown || reason != "api."+siteHost+" failed repeatedly" {
		t.Fatalf("a claimed host's open circuit is the connector's cooldown, got %s %q", left, reason)
	}
	if left, _ := CooldownOf(other, time.Now()); left != 0 {
		t.Fatalf("another site's circuit is not this one's, got %s", left)
	}

	// The connector's own breaker wins when it lasts longer, and the panel
	// reports the same thing the poller goes by.
	site.left, site.reason = time.Hour, "Cloudflare refused us (403 streak)"
	if left, reason := CooldownOf(site, time.Now()); left != time.Hour || reason != site.reason {
		t.Fatalf("the longer cooldown must be reported, got %s %q", left, reason)
	}
	registry := NewRegistry()
	if err := registry.Register(other); err != nil {
		t.Fatalf("register: %v", err)
	}
	NoteHostRateLimited(otherHost)
	row := registry.SourceHealthReport(time.Now()).Sources[0]
	if row.State != SourceStateDegraded || row.CooldownLeft <= 0 || row.CooldownReason != otherHost+" failed repeatedly" {
		t.Fatalf("the panel must show the shared circuit: %+v", row)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
