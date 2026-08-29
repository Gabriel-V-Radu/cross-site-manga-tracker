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
// reader URLs offline, the way the MangaFire connector can.
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
// latest with the typed verdict — and can also build reader URLs offline, the
// way the live MangaFire connector behaves when its site answers.
type linkableCappedConnector struct {
	cappedResolverConnector
}

func (l linkableCappedConnector) BuildChapterURL(rawURL string, chapter float64) (string, bool) {
	return rawURL + "/read/chapter-" + strconv.FormatFloat(chapter, 'f', -1, 64), true
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
