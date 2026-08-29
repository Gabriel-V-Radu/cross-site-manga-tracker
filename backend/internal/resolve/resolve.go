// Package resolve answers the two questions a tracker card cannot be rendered
// without: where its cover art lives, and where a chapter opens. Both answers
// come from a cache and, on a miss, from a background fetch against the
// tracker's linked sources — none of which is HTTP. It lives outside the
// handler package because the handler's only part in it is asking the question
// and rendering the answer.
package resolve

import (
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// How long a failed lookup is remembered. These exist to stop a page of failing
// trackers from re-querying their sources every couple of minutes for as long as
// the page stays open: the sources this app reads are exactly the ones that
// respond to being hammered by putting up a bot challenge, so a failure is held
// long enough to be worth something. The short span still recovers from an
// outage within one sitting.
const (
	lookupRetryTTL       = 10 * time.Minute
	lookupUnreachableTTL = 30 * time.Minute
)

// jitteredTTL spreads expiry by up to a quarter of the span. A page's worth of
// covers fails at the same moment and would otherwise expire at the same moment,
// turning every retry into a synchronized burst against one site.
func jitteredTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	return ttl + time.Duration(rand.Int64N(int64(ttl/4)+1))
}

// cacheEntry is what the shared cache below holds. Staleness is the only thing
// it and its sweeper need to know about an entry, and the two resolvers
// disagree on what stale means — a cover whose image sits on disk never is — so
// each entry answers for itself.
type cacheEntry interface {
	expired(now time.Time) bool
}

// resultCache is the keyed store both resolvers answer from. It holds failures
// as well as successes: remembering that a source had nothing is what the TTLs
// above are for.
type resultCache[V cacheEntry] struct {
	mu      sync.RWMutex
	entries map[string]V
}

func newResultCache[V cacheEntry]() *resultCache[V] {
	return &resultCache[V]{entries: make(map[string]V)}
}

func (c *resultCache[V]) get(key string) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, exists := c.entries[key]
	return entry, exists
}

func (c *resultCache[V]) put(key string, entry V) {
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
}

func (c *resultCache[V]) drop(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *resultCache[V]) reset() {
	c.mu.Lock()
	c.entries = make(map[string]V)
	c.mu.Unlock()
}

func (c *resultCache[V]) dropWhere(match func(V) bool) {
	c.mu.Lock()
	for key, entry := range c.entries {
		if match(entry) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// sweepExpired only removes entries that already read as misses, so it cannot
// change what a cached lookup answers.
func (c *resultCache[V]) sweepExpired(now time.Time) {
	c.dropWhere(func(entry V) bool { return entry.expired(now) })
}

// PageGate names the trackers page the reader is currently looking at, so a
// background fetch queued for a page they have since navigated away from is
// abandoned instead of spending a connector slot on a card nobody is waiting
// for. One gate is shared by both resolvers: there is only ever one such page.
type PageGate struct {
	mu  sync.RWMutex
	key string
}

func NewPageGate() *PageGate {
	return &PageGate{}
}

func (g *PageGate) SetActive(pageKey string) {
	g.mu.Lock()
	g.key = strings.TrimSpace(pageKey)
	g.mu.Unlock()
}

func (g *PageGate) IsActive(pageKey string) bool {
	g.mu.RLock()
	active := g.key
	g.mu.RUnlock()
	return strings.TrimSpace(pageKey) != "" && strings.TrimSpace(pageKey) == strings.TrimSpace(active)
}

// fetchQueue runs at most one background fetch per cache key, bounded by a
// semaphore the caller picks. Without the in-flight guard a page rendering the
// same title twice queues the same fetch twice; without the semaphore a page
// load opens one connection per card at once, which is how these sites decide
// they are being scraped.
type fetchQueue struct {
	mu       sync.Mutex
	inFlight map[string]bool
	gate     *PageGate
}

func newFetchQueue(gate *PageGate) *fetchQueue {
	if gate == nil {
		gate = NewPageGate()
	}
	return &fetchQueue{inFlight: make(map[string]bool), gate: gate}
}

// run starts work in the background unless the same key is already being
// fetched. An empty pageKey means the caller is not tied to a rendered page, so
// there is nothing to navigate away from and the work always runs.
func (q *fetchQueue) run(key string, sem chan struct{}, pageKey string, work func()) {
	q.mu.Lock()
	if q.inFlight[key] {
		q.mu.Unlock()
		return
	}
	q.inFlight[key] = true
	q.mu.Unlock()

	go func() {
		sem <- struct{}{}
		defer func() {
			<-sem
			q.mu.Lock()
			delete(q.inFlight, key)
			q.mu.Unlock()
		}()

		if pageKey != "" && !q.gate.IsActive(pageKey) {
			return
		}

		work()
	}()
}

// sweepInterval paces the sweep below. Nothing it drops is urgent — those
// entries are already dead, merely unreferenced — so the sweep is paced to stay
// invisible on the Pi rather than to reclaim promptly.
const sweepInterval = 15 * time.Minute

// sweeper drops expired entries in the background. The caches only ever evict
// on a read of the same key, and the keys that go cold are exactly the ones
// nobody reads again — every chapter that ever released, every source URL a
// tracker was relinked away from — so on a process that runs for months they
// are only ever added to.
type sweeper struct {
	stop chan struct{}
	once sync.Once
}

func startSweeper(interval time.Duration, sweep func()) *sweeper {
	swept := &sweeper{stop: make(chan struct{})}
	stop := swept.stop
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
	return swept
}

// Close ends the sweep. Idempotent: shutdown hooks run more than once often
// enough that closing the channel twice would be a panic waiting to happen.
func (s *sweeper) Close() {
	s.once.Do(func() { close(s.stop) })
}
