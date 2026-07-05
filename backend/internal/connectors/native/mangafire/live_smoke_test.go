//go:build live

package mangafire

import (
	"context"
	"testing"
	"time"
)

// Live smoke test against the real site. Run with: go test -tags live -run TestLive -v ./internal/connectors/native/mangafire
func TestLiveMangaFire(t *testing.T) {
	connector := NewConnector()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := connector.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(ctx, "https://mangafire.to/manga/one-piecee.dkw")
	if err != nil {
		t.Fatalf("resolve legacy url failed: %v", err)
	}
	t.Logf("resolved legacy: id=%s title=%q url=%s latest=%v cover=%s updated=%v related=%d",
		resolved.SourceItemID, resolved.Title, resolved.URL, *resolved.LatestChapter, resolved.CoverImageURL, resolved.LastUpdatedAt, len(resolved.RelatedTitles))

	results, err := connector.SearchByTitle(ctx, "blue lock", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected search results")
	}
	for _, item := range results {
		t.Logf("search: id=%s title=%q latest=%v updated=%v", item.SourceItemID, item.Title, item.LatestChapter, item.LastUpdatedAt)
	}

	chapterURL, err := connector.ResolveChapterURL(ctx, resolved.URL, 1186)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	t.Logf("chapter url: %s", chapterURL)
}
