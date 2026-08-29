package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// The sites this app reads answer sustained bursts by putting up a bot
// challenge, and one already has (MangaFire). Every native connector therefore
// shares one transport-level throttle with two behaviours:
//
//   - pacing: requests to the same host are spaced at least hostRequestGap
//     apart, whoever issues them (the poller, a dashboard page, a link scan);
//   - a circuit breaker: after hostFailureThreshold consecutive failures a
//     host is left alone for hostCooldown, so a site that has gone dark fails
//     fast instead of eating a full timeout per tracker per cycle.
//
// The state is keyed by host, and no two connectors share a host, so one
// shared throttler is equivalent to one per connector — minus eight copies.
const (
	hostRequestGap       = 1 * time.Second
	hostFailureThreshold = 5
	// hostCooldown matches the dashboard's lookupRetryTTL rationale: long
	// enough to matter against a site that punishes hammering, short enough to
	// recover from an ordinary outage within one sitting.
	hostCooldown = 10 * time.Minute
	// maxQueueWait caps how far behind the pacing queue a request will wait.
	// A page of covers queues a couple dozen requests against one host at
	// once; the cap must fit that burst draining at the pacing gap, while
	// still bounding a runaway queue.
	maxQueueWait = 60 * time.Second

	// minClientTimeout floors every throttled client's overall timeout. The
	// pacing wait counts toward http.Client.Timeout, so a burst of background
	// fetches (a page of covers) needs room to drain the queue; actual request
	// deadlines are the callers' per-request contexts, which every call path
	// sets.
	minClientTimeout = 90 * time.Second
)

// hostGapOverrides widens the pacing gap for hosts whose published limits are
// tighter than the default. MangaBaka caps search at 30 requests/minute per
// IP; 2.5s keeps a title-by-title link scan safely under it. ComicK publishes
// no limit but its Cloudflare is burst-sensitive — a dozen rapid requests
// earned a temporary 403 streak in testing, while 1 request per 3-4s never
// did.
var hostGapOverrides = map[string]time.Duration{
	"api.mangabaka.org": 2500 * time.Millisecond,
	"api.comick.dev":    3500 * time.Millisecond,
	// Cloudflare on mangafire.to blocks IPs that burst requests; this widened
	// gap is the connector's old private pacing loop moved into the shared
	// throttle so nothing double-paces on top of it.
	"mangafire.to": 1500 * time.Millisecond,
}

// SourceCoolingDownError is returned without touching the network while a
// host's circuit breaker is open. Callers that want to distinguish "the site
// refused us" from "we are deliberately not asking" can errors.As for it.
type SourceCoolingDownError struct {
	Host       string
	RetryAfter time.Duration
}

func (e *SourceCoolingDownError) Error() string {
	return fmt.Sprintf("%s is cooling down after repeated failures (retry in %s)", e.Host, e.RetryAfter.Round(time.Second))
}

type hostState struct {
	nextAllowed         time.Time
	consecutiveFailures int
	blockedUntil        time.Time
}

type throttler struct {
	gap              time.Duration
	gapOverrides     map[string]time.Duration
	failureThreshold int
	cooldown         time.Duration

	mu    sync.Mutex
	hosts map[string]*hostState
}

func (t *throttler) gapFor(host string) time.Duration {
	if override, ok := t.gapOverrides[host]; ok {
		return override
	}
	return t.gap
}

var defaultThrottler = &throttler{
	gap:              hostRequestGap,
	gapOverrides:     hostGapOverrides,
	failureThreshold: hostFailureThreshold,
	cooldown:         hostCooldown,
	hosts:            map[string]*hostState{},
}

// reserve either admits a request — returning how long the caller must wait to
// honour the host's pacing gap — or refuses it because the host is cooling
// down. The slot is claimed under the lock, so concurrent callers queue up at
// gap-length intervals instead of racing through together.
func (t *throttler) reserve(host string, now time.Time) (time.Duration, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.hosts[host]
	if !ok {
		state = &hostState{}
		t.hosts[host] = state
	}

	if state.blockedUntil.After(now) {
		return 0, &SourceCoolingDownError{Host: host, RetryAfter: state.blockedUntil.Sub(now)}
	}

	start := state.nextAllowed
	if start.Before(now) {
		start = now
	}
	if wait := start.Sub(now); wait > maxQueueWait {
		// Refuse without claiming the slot, so the queue cannot grow past the
		// cap. Not counted as a host failure — the host did nothing wrong.
		return 0, &SourceCoolingDownError{Host: host, RetryAfter: wait}
	}
	state.nextAllowed = start.Add(t.gapFor(host))
	return start.Sub(now), nil
}

// observe records a request's outcome. A refused or unreachable host opens the
// circuit after failureThreshold consecutive failures; any success closes it.
func (t *throttler) observe(host string, now time.Time, failed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.hosts[host]
	if !ok {
		state = &hostState{}
		t.hosts[host] = state
	}

	if !failed {
		state.consecutiveFailures = 0
		return
	}

	state.consecutiveFailures++
	if state.consecutiveFailures >= t.failureThreshold {
		state.blockedUntil = now.Add(t.cooldown)
		state.consecutiveFailures = 0
	}
}

type throttledTransport struct {
	base      http.RoundTripper
	throttler *throttler
}

func (t *throttledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()

	wait, err := t.throttler.reserve(host, time.Now())
	if err != nil {
		return nil, err
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}

	res, err := t.base.RoundTrip(req)
	t.throttler.observe(host, time.Now(), isThrottleFailure(res, err))
	return res, err
}

// isThrottleFailure classifies an outcome for the circuit breaker. A 404 is a
// valid answer (the series is gone), not a failing site; what opens the
// circuit is the site being unreachable, refusing us, or dying server-side.
// A caller giving up (its context cancelled or timed out) says nothing about
// the host, so it must not count toward opening the circuit.
func isThrottleFailure(res *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		return true
	}
	switch {
	case res.StatusCode == http.StatusForbidden,
		res.StatusCode == http.StatusTooManyRequests,
		res.StatusCode >= 500:
		return true
	default:
		return false
	}
}

// ThrottleTransport wraps base with the shared per-host throttle. Connectors
// that need a custom transport (freewebnovel's TLS-fingerprint dialer) wrap it
// here so their traffic is paced like everyone else's.
func ThrottleTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &throttledTransport{base: base, throttler: defaultThrottler}
}

// NewThrottledClient is the client every connector's default constructor uses.
// Tests keep injecting plain clients through the WithOptions constructors and
// stay unpaced. The client timeout is minClientTimeout for everyone: with
// pacing in the transport, a shorter timeout would cut down requests that are
// merely waiting for their slot behind a burst; actual request deadlines are
// the callers' per-request contexts, which every call path sets.
func NewThrottledClient() *http.Client {
	return &http.Client{
		Timeout:   minClientTimeout,
		Transport: ThrottleTransport(nil),
	}
}

// NoteHostRateLimited opens the shared circuit for host for the standard
// cooldown. It exists for rate limits the transport cannot see: MangaHub
// answers them as HTTP 200 with a GraphQL errors array, so the throttle's
// own status classification never fires. Counting it as one more consecutive
// failure would not work either — the transport already recorded the 200 as
// a success, resetting the streak — so an explicit rate-limit statement from
// the server opens the circuit outright.
func NoteHostRateLimited(host string) {
	defaultThrottler.block(host, time.Now())
}

// block opens the circuit for host immediately, without waiting for the
// failure threshold.
func (t *throttler) block(host string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.hosts[host]
	if !ok {
		state = &hostState{}
		t.hosts[host] = state
	}

	if until := now.Add(t.cooldown); until.After(state.blockedUntil) {
		state.blockedUntil = until
	}
	state.consecutiveFailures = 0
}
