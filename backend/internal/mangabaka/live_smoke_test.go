//go:build live

package mangabaka

import (
	"context"
	"testing"
	"time"
)

// Live smoke test against the real API. Run with: go test -tags live -run TestLive -count=1 -v ./internal/mangabaka
// Pass -count=1, otherwise Go replays a cached result and a live outage looks like a pass.
func TestLiveMangaBaka(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results, err := client.Search(ctx, "nano machine", 8)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}

	first := results[0]
	if first.MangaUpdatesID == "" {
		t.Fatalf("expected a mangaupdates id for %q", first.Title)
	}
	t.Logf("id=%d title=%q mu=%s titles=%d", first.ID, first.Title, first.MangaUpdatesURL(), len(first.Titles))
}
