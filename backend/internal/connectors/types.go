package connectors

import (
	"context"
	"time"
)

const (
	KindNative = "native"
)

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

// OfflineChapterLinker builds a best-effort reader URL for a chapter from the
// series URL alone, without touching the network. It exists for sites whose
// reader URL scheme is derivable but whose API sits behind a bot challenge
// the server cannot pass while a human's browser can: the constructed link is
// not verified to exist, which is why resolved URLs always take precedence.
type OfflineChapterLinker interface {
	BuildChapterURL(rawURL string, chapter float64) (string, bool)
}
