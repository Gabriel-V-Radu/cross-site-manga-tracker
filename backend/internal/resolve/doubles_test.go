package resolve

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// siteTier is the SiteInfo a double publishes. The reading chain reads each
// site's tier off its connector, so a double standing in for a ranked site
// (the fresh aggregator, the info floor) has to say which one it is, exactly
// as the real connectors do. Left unset it is the origin tier, which is what a
// double standing in for a tracker's own primary wants. The hosts stay empty:
// these doubles are reached by key, and claiming a host would enter them into
// the registry's URL routing for no reason.
type siteTier struct{ rank int }

func (s siteTier) Hosts() []string { return nil }
func (s siteTier) HomeURL() string { return "" }
func (s siteTier) ReaderRank() int { return s.rank }

// blockedConnector stands in for a source behind a bot challenge: it is
// registered and reachable in principle, but every resolve fails.
type blockedConnector struct {
	siteTier
	key string
}

func (b blockedConnector) Key() string                       { return b.key }
func (b blockedConnector) Name() string                      { return b.key }
func (b blockedConnector) Kind() string                      { return connectors.KindNative }
func (b blockedConnector) HealthCheck(context.Context) error { return errors.New("blocked") }
func (b blockedConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (b blockedConnector) ResolveByURL(context.Context, string) (*connectors.MangaResult, error) {
	return nil, errors.New("behind a browser challenge")
}
func (b blockedConnector) ResolveChapterURL(context.Context, string, float64) (string, error) {
	return "", errors.New("behind a browser challenge")
}

// mirrorConnector stands in for a working alternate source.
type mirrorConnector struct {
	siteTier
	key   string
	cover string
}

func (m mirrorConnector) Key() string                       { return m.key }
func (m mirrorConnector) Name() string                      { return m.key }
func (m mirrorConnector) Kind() string                      { return connectors.KindNative }
func (m mirrorConnector) HealthCheck(context.Context) error { return nil }
func (m mirrorConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}
func (m mirrorConnector) ResolveByURL(_ context.Context, rawURL string) (*connectors.MangaResult, error) {
	return &connectors.MangaResult{SourceKey: m.key, URL: rawURL, CoverImageURL: m.cover}, nil
}
func (m mirrorConnector) ResolveChapterURL(_ context.Context, rawURL string, chapter float64) (string, error) {
	return rawURL + "/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), nil
}

// blockedLinkableConnector is a blocked source that can still construct its
// reader URLs offline, the way a site whose reader URL needs nothing but the
// series URL and the chapter number can. No live connector qualifies right now
// (see connectors.OfflineChapterLinker), so the tier below is pinned here.
type blockedLinkableConnector struct{ blockedConnector }

func (b blockedLinkableConnector) BuildChapterURL(rawURL string, chapter float64) (string, bool) {
	return rawURL + "/read/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), true
}

// cappedResolverConnector resolves chapter URLs only up to a latest-known
// number, the way the MangaHub connector's range check refuses chapters the
// site does not carry yet.
type cappedResolverConnector struct {
	mirrorConnector
	latest float64
}

func (c cappedResolverConnector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if chapter > c.latest {
		return "", fmt.Errorf("chapter beyond latest: %w", connectors.ErrChapterNotFound)
	}
	return c.mirrorConnector.ResolveChapterURL(ctx, rawURL, chapter)
}

// linkableCappedConnector answers its resolver — refusing chapters beyond its
// latest with the typed verdict — and can also build reader URLs offline: the
// combination that must never let a refusal be papered over with a guess.
type linkableCappedConnector struct {
	cappedResolverConnector
}

func (l linkableCappedConnector) BuildChapterURL(rawURL string, chapter float64) (string, bool) {
	return rawURL + "/read/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), true
}

// gatedConnector holds every resolve until the test closes release, so a test
// can act while a background fetch is provably in flight. Released, it answers
// the way mirrorConnector does, or fails the way blockedConnector does when
// fail is set.
type gatedConnector struct {
	siteTier
	key     string
	fail    bool
	started chan struct{}
	release chan struct{}
}

func newGatedConnector(key string, fail bool) *gatedConnector {
	return &gatedConnector{key: key, fail: fail, started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (g *gatedConnector) Key() string                       { return g.key }
func (g *gatedConnector) Name() string                      { return g.key }
func (g *gatedConnector) Kind() string                      { return connectors.KindNative }
func (g *gatedConnector) HealthCheck(context.Context) error { return nil }
func (g *gatedConnector) SearchByTitle(context.Context, string, int) ([]connectors.MangaResult, error) {
	return nil, nil
}

func (g *gatedConnector) wait(ctx context.Context) error {
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if g.fail {
		return errors.New("behind a browser challenge")
	}
	return nil
}

func (g *gatedConnector) ResolveByURL(ctx context.Context, rawURL string) (*connectors.MangaResult, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return &connectors.MangaResult{SourceKey: g.key, URL: rawURL, CoverImageURL: "https://cdn.example/" + g.key + ".webp"}, nil
}

func (g *gatedConnector) ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error) {
	if err := g.wait(ctx); err != nil {
		return "", err
	}
	return rawURL + "/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), nil
}

func waitGateStarted(t *testing.T, connector *gatedConnector) {
	t.Helper()
	select {
	case <-connector.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the background fetch never reached the connector")
	}
}

// waitQueueIdle waits until no background fetch is queued or running. The
// in-flight mark is cleared only after the work returns, so once the queue is
// idle every released fetch has written its answer, or declined to.
func waitQueueIdle(t *testing.T, queue *fetchQueue) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		queue.mu.Lock()
		idle := len(queue.inFlight) == 0
		queue.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the background fetches never finished")
}

// newCoverTestResolver builds a cover resolver that never touches an image
// host: the injected checker stands in for the download, which is what these
// tests would otherwise aim at whatever CDN a fixture names.
func newCoverTestResolver(t *testing.T, registry *connectors.Registry) *CoverResolver {
	t.Helper()
	resolver := NewCoverResolver(CoverConfig{
		Registry:   registry,
		URLChecker: func(context.Context, string) bool { return true },
	})
	t.Cleanup(resolver.Close)
	return resolver
}

func newChapterTestResolver(t *testing.T, registry *connectors.Registry) *ChapterLinkResolver {
	t.Helper()
	resolver := NewChapterLinkResolver(ChapterConfig{Registry: registry})
	t.Cleanup(resolver.Close)
	return resolver
}

// maxJitteredTTL is the ceiling jitteredTTL can return for a span, which is what
// a caller can assert against without depending on the random component.
func maxJitteredTTL(ttl time.Duration) time.Duration {
	return ttl + ttl/4
}
