//go:build live

package freewebnovel

import (
	"context"
	"testing"
	"time"
)

// Live smoke test against the real site. Run with: go test -tags live -run TestLive -v ./internal/connectors/native/freewebnovel
func TestLiveFreeWebNovel(t *testing.T) {
	connector := NewConnector()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := connector.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := connector.ResolveByURL(ctx, "https://freewebnovel.com/novel/star-odyssey")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.Title == "" {
		t.Fatalf("expected a title")
	}
	if resolved.LatestChapter == nil {
		t.Fatalf("expected a latest chapter")
	}
	t.Logf("resolved: id=%s title=%q url=%s latest=%v cover=%s updated=%v related=%v",
		resolved.SourceItemID, resolved.Title, resolved.URL, *resolved.LatestChapter, resolved.CoverImageURL, resolved.LastUpdatedAt, resolved.RelatedTitles)

	results, err := connector.SearchByTitle(ctx, "star odyssey", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected search results")
	}
	for _, item := range results {
		t.Logf("search: id=%s title=%q latest=%v cover=%s", item.SourceItemID, item.Title, item.LatestChapter, item.CoverImageURL)
	}

	chapterURL, err := connector.ResolveChapterURL(ctx, resolved.URL, *resolved.LatestChapter)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	t.Logf("chapter url: %s", chapterURL)

	// Titles on the live site contain raw apostrophes inside double-quoted
	// og: meta attributes; make sure they are not truncated at the apostrophe.
	apostrophe, err := connector.ResolveByURL(ctx, "https://freewebnovel.com/novel/the-authors-pov")
	if err != nil {
		t.Fatalf("resolve apostrophe title failed: %v", err)
	}
	if apostrophe.Title != "The Author's POV" {
		t.Fatalf("expected full apostrophe title \"The Author's POV\", got %q", apostrophe.Title)
	}
	t.Logf("apostrophe title resolved: %q", apostrophe.Title)
}
