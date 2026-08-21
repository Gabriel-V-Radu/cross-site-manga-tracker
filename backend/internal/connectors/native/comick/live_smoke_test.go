//go:build live

package comick

import (
	"context"
	"testing"
	"time"
)

// Run with: go test -tags live -count=1 ./internal/connectors/native/comick
func TestLiveResolveKagurabachi(t *testing.T) {
	connector := NewConnector()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := connector.ResolveByURL(ctx, "https://comick.dev/comic/kagura-bachi")
	if err != nil {
		t.Fatalf("live resolve failed: %v", err)
	}
	if result.Title == "" {
		t.Fatalf("expected a title")
	}
	if result.LatestChapter == nil || *result.LatestChapter < 128 {
		t.Fatalf("expected latest chapter >= 128, got %#v", result.LatestChapter)
	}
	if result.LastUpdatedAt == nil {
		t.Fatalf("expected a publish timestamp")
	}
	t.Logf("live: %q chapter %.1f at %v", result.Title, *result.LatestChapter, result.LastUpdatedAt)
}

func TestLiveSearchAndChapterURL(t *testing.T) {
	connector := NewConnector()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	results, err := connector.SearchByTitle(ctx, "sakamoto days", 5)
	if err != nil {
		t.Fatalf("live search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected search results")
	}

	url, err := connector.ResolveChapterURL(ctx, "https://comick.dev/comic/kagura-bachi", 128)
	if err != nil {
		t.Fatalf("live chapter url failed: %v", err)
	}
	t.Logf("live chapter url: %s", url)
}
