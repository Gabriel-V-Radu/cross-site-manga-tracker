package resolve

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// hangingConnector blocks every resolve until its context is cancelled, the
// way a site that has stopped answering does, and records how the wait ended.
type hangingConnector struct {
	siteTier
	key       string
	started   chan struct{}
	cancelled atomic.Bool
}

func (h *hangingConnector) Key() string                       { return h.key }
func (h *hangingConnector) Name() string                      { return h.key }
func (h *hangingConnector) Kind() string                      { return connectors.KindNative }
func (h *hangingConnector) HealthCheck(context.Context) error { return nil }
func (h *hangingConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

func (h *hangingConnector) hang(ctx context.Context) error {
	select {
	case h.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	h.cancelled.Store(true)
	return ctx.Err()
}

func (h *hangingConnector) ResolveByURL(ctx context.Context, _ string) (*connectors.MangaResult, error) {
	return nil, h.hang(ctx)
}

func (h *hangingConnector) ResolveChapterURL(ctx context.Context, _ string, _ float64) (string, error) {
	return "", h.hang(ctx)
}

func newHangingRegistry(t *testing.T) (*connectors.Registry, *hangingConnector) {
	t.Helper()
	registry := connectors.NewRegistry()
	connector := &hangingConnector{key: "hangingsource", started: make(chan struct{}, 1)}
	if err := registry.Register(connector); err != nil {
		t.Fatalf("register: %v", err)
	}
	return registry, connector
}

func waitStarted(t *testing.T, connector *hangingConnector) {
	t.Helper()
	select {
	case <-connector.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the background fetch never reached the connector")
	}
}

// Close cancels a cover fetch that is still waiting on its site and returns
// once it has unwound. Before this the fetch ran on a context nothing owned:
// shutdown closed the database under it and it kept the connection to the site
// open for its full timeout from a process that was already gone.
func TestCoverResolverCloseCancelsInFlightFetch(t *testing.T) {
	registry, connector := newHangingRegistry(t)
	resolver := NewCoverResolver(CoverConfig{
		Registry:   registry,
		URLChecker: func(context.Context, string) bool { return true },
	})

	if _, _, waiting := resolver.Lookup("hangingsource", "https://hanging.example/title/a", nil, nil, ""); !waiting {
		t.Fatal("expected a background fetch to be queued")
	}
	waitStarted(t, connector)

	closed := make(chan struct{})
	go func() {
		resolver.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(closeGrace + time.Second):
		t.Fatal("Close did not return within the grace period")
	}
	if !connector.cancelled.Load() {
		t.Fatal("the in-flight fetch was not cancelled by Close")
	}

	// Nothing is queued after Close.
	if _, _, waiting := resolver.Lookup("hangingsource", "https://hanging.example/title/b", nil, nil, ""); !waiting {
		// Lookup still reports waiting — the answer is not cached — but the
		// queue refuses the work, which is what the connector below verifies.
		t.Fatal("expected Lookup to report a pending fetch")
	}
	select {
	case <-connector.started:
		t.Fatal("a fetch was started after Close")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestChapterLinkResolverCloseCancelsInFlightFetch(t *testing.T) {
	registry, connector := newHangingRegistry(t)
	resolver := NewChapterLinkResolver(ChapterConfig{Registry: registry})

	if _, _, waiting := resolver.Lookup("hangingsource", "https://hanging.example/title/a", 12, nil, ""); !waiting {
		t.Fatal("expected a background resolve to be queued")
	}
	waitStarted(t, connector)

	closed := make(chan struct{})
	go func() {
		resolver.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(closeGrace + time.Second):
		t.Fatal("Close did not return within the grace period")
	}
	if !connector.cancelled.Load() {
		t.Fatal("the in-flight resolve was not cancelled by Close")
	}
}

// A fetch still waiting for a semaphore slot at shutdown returns instead of
// blocking Close for the whole grace period.
func TestFetchQueueCloseReleasesFetchesWaitingForASlot(t *testing.T) {
	queue := newFetchQueue(nil)
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // the only slot is taken

	ran := make(chan struct{}, 1)
	queue.run("k", sem, "", func(context.Context) { ran <- struct{}{} })

	started := time.Now()
	queue.close()
	if took := time.Since(started); took >= closeGrace {
		t.Fatalf("close took %v; a fetch waiting for a slot must return on cancellation", took)
	}
	select {
	case <-ran:
		t.Fatal("work must not run after close")
	default:
	}
}

// Expiry-on-read must not evict an entry a fetch refreshed in the meantime:
// dropIf re-checks under the write lock.
func TestResultCacheDropIfKeepsARefreshedEntry(t *testing.T) {
	cache := newResultCache[chapterEntry]()
	now := time.Now().UTC()
	cache.put("k", chapterEntry{ChapterURL: "stale", ExpiresAt: now.Add(-time.Minute)})

	stale, _ := cache.get("k")
	if !stale.expired(now) {
		t.Fatal("fixture entry should read as expired")
	}
	// A background fetch lands a fresh answer before the eviction runs.
	cache.put("k", chapterEntry{ChapterURL: "fresh", ExpiresAt: now.Add(time.Hour)})

	if cache.dropIf("k", func(entry chapterEntry) bool { return entry.expired(now) }) {
		t.Fatal("dropIf evicted an entry that was no longer expired")
	}
	if entry, ok := cache.get("k"); !ok || entry.ChapterURL != "fresh" {
		t.Fatalf("fresh entry lost: %+v ok=%v", entry, ok)
	}

	// The unconditional case still evicts.
	if !cache.dropIf("k", func(chapterEntry) bool { return true }) {
		t.Fatal("dropIf should evict when the predicate holds")
	}
	if _, ok := cache.get("k"); ok {
		t.Fatal("entry should be gone")
	}
}

// Run after close does nothing rather than starting work on a dead context.
func TestFetchQueueRefusesWorkAfterClose(t *testing.T) {
	queue := newFetchQueue(nil)
	queue.close()
	ran := make(chan struct{}, 1)
	queue.run("k", make(chan struct{}, 1), "", func(ctx context.Context) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("work ran on a live context after close")
		}
		ran <- struct{}{}
	})
	select {
	case <-ran:
		t.Fatal("work ran after close")
	case <-time.After(100 * time.Millisecond):
	}
}
