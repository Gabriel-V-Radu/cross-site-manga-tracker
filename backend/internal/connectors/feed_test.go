package connectors

import (
	"context"
	"testing"
	"time"
)

// A listing names uploads, not series: FeedRows folds them into one row per
// series at its highest chapter, and drops what is not a usable chapter.
func TestFeedRowsKeepOneRowPerSeriesAtItsHighestChapter(t *testing.T) {
	early := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)

	var rows FeedRows
	rows.Add("b", 12, &early, MaxPlausibleChapter)
	rows.Add("b", 13, &late, MaxPlausibleChapter)
	rows.Add("b", 12.5, &late, MaxPlausibleChapter)
	rows.Add(" a ", 7, nil, MaxPlausibleChapter)
	rows.Add("", 3, nil, MaxPlausibleChapter)
	rows.Add("junk", 2019_000, nil, MaxPlausibleChapter)
	rows.Add("zero", 0, nil, MaxPlausibleChapter)

	got := rows.Result()
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want a and b only", got)
	}
	if got[0].Key != "a" || got[0].Chapter != 7 || got[0].UpdatedAt != nil {
		t.Fatalf("first row = %+v, want a trimmed key at chapter 7", got[0])
	}
	if got[1].Key != "b" || got[1].Chapter != 13 || got[1].UpdatedAt == nil || !got[1].UpdatedAt.Equal(late) {
		t.Fatalf("second row = %+v, want b at 13 with that chapter's own date", got[1])
	}

	var empty FeedRows
	if result := empty.Result(); len(result) != 0 {
		t.Fatalf("an empty listing has no rows, got %+v", result)
	}
}

type keyedConnector struct{ key string }

func (k keyedConnector) Key() string                       { return k.key }
func (k keyedConnector) Name() string                      { return k.key }
func (k keyedConnector) Kind() string                      { return KindNative }
func (k keyedConnector) HealthCheck(context.Context) error { return nil }
func (k keyedConnector) ResolveByURL(context.Context, string) (*MangaResult, error) {
	return nil, nil
}
func (k keyedConnector) SearchByTitle(context.Context, string, int) ([]MangaResult, error) {
	return nil, nil
}

func TestRegistryConnectorsAreListedByKey(t *testing.T) {
	registry := NewRegistry()
	for _, key := range []string{"gamma", "alpha", "beta"} {
		if err := registry.Register(keyedConnector{key: key}); err != nil {
			t.Fatalf("register %s: %v", key, err)
		}
	}
	list := registry.Connectors()
	if len(list) != 3 || list[0].Key() != "alpha" || list[1].Key() != "beta" || list[2].Key() != "gamma" {
		t.Fatalf("connectors not listed by key: %v", list)
	}
}
