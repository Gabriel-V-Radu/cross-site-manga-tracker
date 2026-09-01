package connectors

import (
	"context"
	"errors"
	"time"
)

const (
	KindNative = "native"
)

// ErrChapterNotFound is what a ChapterURLResolver wraps when the site
// answered and the answer was "I do not carry this chapter". Callers walking
// a fallback chain need it to tell that verdict apart from "the site could
// not be asked": a site that answered must simply cede its turn, while an
// unreachable one may still claim it with an offline-built link (see
// OfflineChapterLinker).
var ErrChapterNotFound = errors.New("chapter not found")

type MangaResult struct {
	SourceKey     string     `json:"sourceKey"`
	SourceItemID  string     `json:"sourceItemId"`
	Title         string     `json:"title"`
	RelatedTitles []string   `json:"relatedTitles,omitempty"`
	URL           string     `json:"url"`
	CoverImageURL string     `json:"coverImageUrl,omitempty"`
	LatestChapter *float64   `json:"latestChapter,omitempty"`
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
}

type Connector interface {
	Key() string
	Name() string
	Kind() string
	HealthCheck(ctx context.Context) error
	ResolveByURL(ctx context.Context, rawURL string) (*MangaResult, error)
	SearchByTitle(ctx context.Context, title string, limit int) ([]MangaResult, error)
}

type ChapterURLResolver interface {
	ResolveChapterURL(ctx context.Context, rawURL string, chapter float64) (string, error)
}

// CooldownReporter is implemented by connectors that keep their own circuit
// breaker (MangaFire classifies Cloudflare responses the shared per-host
// throttle cannot see). CooldownRemaining returns how much longer the
// connector will refuse requests and why; zero or negative means it is
// accepting requests. The poller consults it before resolving so a site known
// to be dark is skipped outright instead of burning a doomed request — and a
// log line — per tracker.
type CooldownReporter interface {
	CooldownRemaining() (time.Duration, string)
}

// OfflineChapterLinker builds a best-effort reader URL for a chapter from the
// series URL alone, without touching the network. It exists for sites whose
// reader URL scheme is derivable but whose API sits behind a bot challenge
// the server cannot pass while a human's browser can. The constructed link is
// not verified to exist, so it ranks below every site that verified it
// carries the chapter, but above the info-floor sites nobody wants to read
// on: the human's browser passes the challenge the server cannot, so a built
// link beats ComicK's resolved one. It only stands in for a site that could
// not be asked — a site whose resolver answered ErrChapterNotFound has ceded
// its turn, and papering over that refusal would link to a page that is known
// not to exist.
//
// No connector implements this today. MangaFire did until the site retired the
// derivable /read/{slug}.{hid}/en/chapter-{n} scheme and started redirecting it
// to the series page, which turned every built link into a chapter link that
// opened the wrong page (see that connector's ResolveChapterURL). The interface
// and the resolver tier that uses it are kept because the situation it answers
// — a readable site the server cannot query, whose reader URL needs nothing but
// the series URL — is one this app keeps running into. A site is only eligible
// if its reader URL carries no identifier that has to be looked up first.
type OfflineChapterLinker interface {
	BuildChapterURL(rawURL string, chapter float64) (string, bool)
}
