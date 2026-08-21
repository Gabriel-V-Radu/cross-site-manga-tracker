//go:build live

package weebcentral

import (
	"context"
	"testing"
	"time"
)

// Live smoke test against the real site. Run with: go test -tags live -run TestLive -count=1 -v ./internal/connectors/native/weebcentral
// Pass -count=1, otherwise Go replays a cached result and a live outage looks like a pass.
func TestLiveWeebCentral(t *testing.T) {
	connector := NewConnector()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := connector.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(ctx, "https://weebcentral.com/series/01J76XYDT7H7ANER8KJG5R9SJV/Nano-Machine")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil {
		t.Fatalf("expected a latest chapter for Nano Machine")
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected a release timestamp for Nano Machine")
	}
	t.Logf("resolved: id=%s title=%q latest=%.1f released=%s cover=%s",
		resolved.SourceItemID, resolved.Title, *resolved.LatestChapter, resolved.LastUpdatedAt, resolved.CoverImageURL)

	results, err := connector.SearchByTitle(ctx, "blue lock", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected search results")
	}
	for _, item := range results {
		t.Logf("search: id=%s title=%q url=%s", item.SourceItemID, item.Title, item.URL)
	}

	chapterURL, err := connector.ResolveChapterURL(ctx, resolved.URL, 300)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	t.Logf("chapter url: %s", chapterURL)
}
