package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
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

	// MinClientTimeout floors every throttled client's overall timeout. The
	// pacing wait counts toward http.Client.Timeout, so a burst of background
	// fetches (a page of covers) needs room to drain the queue; actual request
	// deadlines are the callers' per-request contexts, which every call path
	// sets. Exported for the one connector that builds its own client around
	// ThrottleTransport (freewebnovel's TLS-fingerprint dialer): a shorter
	// client timeout there cut down requests that were merely queued.
	MinClientTimeout = 90 * time.Second
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
	// QueueFull marks a refusal that has nothing to do with the host: this
	// process already has more requests queued for it than the pacing gap can
	// drain within maxQueueWait. It used to read "cooling down after repeated
	// failures" like a real outage, so a burst of cover lookups made a healthy
	// site look broken to anything that classified the error.
	QueueFull bool
}

func (e *SourceCoolingDownError) Error() string {
	if e.QueueFull {
		return fmt.Sprintf("%s request queue is full locally (would wait %s)", e.Host, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("%s is cooling down after repeated failures (retry in %s)", e.Host, e.RetryAfter.Round(time.Second))
}

type hostState struct {
	nextAllowed         time.Time
	consecutiveFailures int
	blockedUntil        time.Time
	// onProbation is set when the circuit opens and cleared by the first
	// success after it: until then the host has not shown it is back, and one
	// more failure is enough to open the circuit again (see observe).
	onProbation bool
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

// reserve either admits a request — returning the slot it may start at, and so
// how long the caller must wait to honour the host's pacing gap — or refuses it
// because the host is cooling down. The slot is claimed under the lock, so
// concurrent callers queue up at gap-length intervals instead of racing through
// together. A caller that gives up before its slot arrives must hand it back
// with release.
func (t *throttler) reserve(host string, now time.Time) (slot time.Time, wait time.Duration, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.hosts[host]
	if !ok {
		state = &hostState{}
		t.hosts[host] = state
	}

	if state.blockedUntil.After(now) {
		return time.Time{}, 0, &SourceCoolingDownError{Host: host, RetryAfter: state.blockedUntil.Sub(now)}
	}

	start := state.nextAllowed
	if start.Before(now) {
		start = now
	}
	if wait := start.Sub(now); wait > maxQueueWait {
		// Refuse without claiming the slot, so the queue cannot grow past the
		// cap. Not counted as a host failure — the host did nothing wrong.
		return time.Time{}, 0, &SourceCoolingDownError{Host: host, RetryAfter: wait, QueueFull: true}
	}
	state.nextAllowed = start.Add(t.gapFor(host))
	return start, start.Sub(now), nil
}

// release gives back a slot whose request never went out. The queue only ever
// grows from the tail, so the slot can be reclaimed exactly when it is still
// the last one claimed: nextAllowed is then rolled back to it. A slot with
// later requests already queued behind it is left alone — reclaiming it would
// mean shifting every one of them, and the gap it leaves drains on its own.
// Without this, a page of cover fetches abandoned by a navigation left a minute
// of phantom pacing on the host, and the poller's next resolve queued behind
// requests that never happened.
func (t *throttler) release(host string, slot time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.hosts[host]
	if !ok {
		return
	}
	if state.nextAllowed.Equal(slot.Add(t.gapFor(host))) {
		state.nextAllowed = slot
	}
}

// observe records a request's outcome. A refused or unreachable host opens the
// circuit after failureThreshold consecutive failures; any success closes it.
//
// A circuit that has just expired is on probation: the first request through
// is a probe, and if it fails the circuit opens again at once instead of after
// another failureThreshold failures. Without that, a host that stayed dark got
// a fresh run of full-length failures every time its cooldown ran out — five
// dial timeouts of thirty seconds each, per host, every ten minutes, which is
// what stretched a four-minute poll cycle to sixteen during a route outage.
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
		state.onProbation = false
		return
	}

	if state.blockedUntil.After(now) {
		// A request that was already in flight when the circuit opened: the
		// same incident, not new evidence.
		return
	}

	state.consecutiveFailures++
	if state.onProbation || state.consecutiveFailures >= t.failureThreshold {
		state.blockedUntil = now.Add(t.cooldown)
		state.consecutiveFailures = 0
		state.onProbation = true
	}
}

type throttledTransport struct {
	base      http.RoundTripper
	throttler *throttler
}

func (t *throttledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()

	slot, wait, err := t.throttler.reserve(host, time.Now())
	if err != nil {
		return nil, err
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-req.Context().Done():
			t.throttler.release(host, slot)
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}

	res, err := t.base.RoundTrip(req)
	t.throttler.observe(host, time.Now(), isThrottleFailure(req.Context(), res, err))
	return res, err
}

// isThrottleFailure classifies an outcome for the circuit breaker. A 404 is a
// valid answer (the series is gone), not a failing site; what opens the
// circuit is the site being unreachable, refusing us, or dying server-side.
// A caller giving up (its context cancelled or timed out) says nothing about
// the host, so it must not count toward opening the circuit.
//
// Whether the caller gave up is read from the request's context, not from the
// error: Go's dial and read timeouts match context.DeadlineExceeded under
// errors.Is, so testing the error made a host that stopped accepting
// connections indistinguishable from an impatient caller. Such a host never
// opened the circuit, and every tracker on it spent a full dial timeout per
// cycle — seen on the Pi when a Cloudflare address went dark and the Asura and
// MangaFire shards took 30 seconds a tracker.
func isThrottleFailure(ctx context.Context, res *http.Response, err error) bool {
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
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

// connectTimeout bounds opening a connection. net/http's default transport
// allows thirty seconds, which is the timeout of a host that will never
// answer: the sites this app reads sit behind CDNs that complete a handshake
// in well under a second, so a connect still pending after ten is a dead
// route or a dead address, and every further second is a poll shard standing
// still. (With several addresses for a name the dialer splits this budget
// across them.)
const connectTimeout = 10 * time.Second

// sharedDialer opens the connectors' connections: net/http's default dialer
// with connectTimeout in place of its thirty seconds.
var sharedDialer = &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}

// sharedTransport is http.DefaultTransport dialing through sharedDialer,
// shared by every throttled client so connections are pooled across
// connectors exactly as they were on the default transport.
var sharedTransport = func() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Something replaced the default transport with its own type; there is
		// no dialer of ours to put into that.
		return http.DefaultTransport
	}
	transport := base.Clone()
	transport.DialContext = sharedDialer.DialContext
	return transport
}()

// ThrottleTransport wraps base with the shared per-host throttle. Connectors
// that need a custom transport (freewebnovel's TLS-fingerprint dialer) wrap it
// here so their traffic is paced like everyone else's.
func ThrottleTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = sharedTransport
	}
	return &throttledTransport{base: base, throttler: defaultThrottler}
}

// NewThrottledClient is the client every connector's default constructor uses.
// Tests keep injecting plain clients through the WithOptions constructors and
// stay unpaced. The client timeout is MinClientTimeout for everyone: with
// pacing in the transport, a shorter timeout would cut down requests that are
// merely waiting for their slot behind a burst; actual request deadlines are
// the callers' per-request contexts, which every call path sets.
func NewThrottledClient() *http.Client {
	return &http.Client{
		Timeout:       MinClientTimeout,
		Transport:     ThrottleTransport(nil),
		CheckRedirect: RefuseOffSiteRedirect,
	}
}

// RefuseOffSiteRedirect is an http.Client.CheckRedirect that stops a redirect
// chain at the hop that leaves the site first asked, with an
// *OffSiteRedirectError. The page behind such a hop is never the one that was
// asked for — FetchBytes refuses it anyway — so following it only fetched a
// stranger's page to throw away: during a FlameComics outage every poll of
// every Flame tracker also requested a Discord invite page, with this app's
// browser User-Agent. Same-site hops (a www. or m. host, a path change) are
// followed up to net/http's usual limit of ten.
func RefuseOffSiteRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if from, to := via[0].URL.Hostname(), req.URL.Hostname(); !SameSite(from, to) {
		return &OffSiteRedirectError{From: from, To: to}
	}
	return nil
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

// HostCircuit is one host whose shared circuit is open right now.
type HostCircuit struct {
	Host       string
	BlockedFor time.Duration
}

// OpenHostCircuits lists the hosts the shared throttle is refusing at now,
// for the source health panel. It includes hosts no connector claims (cover
// CDNs, MangaBaka); callers map them back to connectors themselves.
func OpenHostCircuits(now time.Time) []HostCircuit {
	return defaultThrottler.openCircuits(now)
}

func (t *throttler) openCircuits(now time.Time) []HostCircuit {
	t.mu.Lock()
	defer t.mu.Unlock()

	var open []HostCircuit
	for host, state := range t.hosts {
		if state.blockedUntil.After(now) {
			open = append(open, HostCircuit{Host: host, BlockedFor: state.blockedUntil.Sub(now)})
		}
	}
	return open
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
	state.onProbation = true
}
