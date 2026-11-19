package mangadex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// A resolve reads the manga record once a day and the English feed every time:
// the record describes the series, the feed is what moves between polls.
func TestMangaDexResolveCachesTheMangaRecord(t *testing.T) {
	var recordHits, feedHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+metadataTitleID, func(w http.ResponseWriter, _ *http.Request) {
		recordHits.Add(1)
		_, _ = w.Write([]byte(daraSanRecord))
	})
	feed := serveFeed([]feedEntry{{id: "c54", chapter: "54", publish: "2026-11-07T00:00:00+00:00"}}, nil)
	mux.HandleFunc("/manga/"+metadataTitleID+"/feed", func(w http.ResponseWriter, r *http.Request) {
		feedHits.Add(1)
		feed(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})

	for poll := 0; poll < 3; poll++ {
		resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID)
		if err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		if resolved.Title != "Reiwa no Dara-san" || resolved.Metadata == nil || resolved.CoverImageURL == "" {
			t.Fatalf("poll %d: the cached record must still give title, cover and metadata, got %+v", poll, resolved)
		}
		if resolved.LatestChapter == nil || *resolved.LatestChapter != 54 {
			t.Fatalf("poll %d: chapter %v, want 54", poll, resolved.LatestChapter)
		}
	}
	if recordHits.Load() != 1 || feedHits.Load() != 3 {
		t.Fatalf("record asked %d times, feed %d; want 1 and 3", recordHits.Load(), feedHits.Load())
	}
}

// A record that could not be fetched is not cached: the next resolve asks again.
func TestMangaDexFailedRecordIsNotCached(t *testing.T) {
	var recordHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+metadataTitleID, func(w http.ResponseWriter, _ *http.Request) {
		if recordHits.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(daraSanRecord))
	})
	mux.HandleFunc("/manga/"+metadataTitleID+"/feed", serveFeed(nil, nil))
	server := httptest.NewServer(mux)
	defer server.Close()
	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})

	if _, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID); err == nil {
		t.Fatal("expected the 503 to fail the first resolve")
	}
	if _, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if recordHits.Load() != 2 {
		t.Fatalf("record asked %d times, want 2", recordHits.Load())
	}
}

// The cover is a relationship of the manga record, so asking for it alone (the
// cover resolver, through connectors.CoverURLResolver) reads no feed, and
// nothing at all while the record is cached. A full resolve per cover spent a
// feed request on an answer nothing looked at.
func TestMangaDexCoverComesFromTheMangaRecordAlone(t *testing.T) {
	var recordHits, feedHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/"+metadataTitleID, func(w http.ResponseWriter, _ *http.Request) {
		recordHits.Add(1)
		_, _ = w.Write([]byte(daraSanRecord))
	})
	feed := serveFeed([]feedEntry{{id: "c54", chapter: "54", publish: "2026-11-07T00:00:00+00:00"}}, nil)
	mux.HandleFunc("/manga/"+metadataTitleID+"/feed", func(w http.ResponseWriter, r *http.Request) {
		feedHits.Add(1)
		feed(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	connector := NewConnectorWithOptions(server.URL, []string{"mangadex.org"}, &http.Client{Timeout: 5 * time.Second})
	var resolver connectors.CoverURLResolver = connector

	var cover string
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		// With the trailing slug stored links carry.
		if cover, err = resolver.ResolveCoverURL(context.Background(), " https://mangadex.org/title/"+metadataTitleID+"/reiwa-no-dara-san "); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if cover == "" {
			t.Fatalf("attempt %d: no cover", attempt)
		}
	}
	if recordHits.Load() != 1 || feedHits.Load() != 0 {
		t.Fatalf("record asked %d times, feed %d; want 1 and 0", recordHits.Load(), feedHits.Load())
	}

	// The same cover a resolve reports, from the record the cover left cached.
	resolved, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/"+metadataTitleID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CoverImageURL != cover || recordHits.Load() != 1 {
		t.Fatalf("resolve reported %q after %d record requests; want %q after 1", resolved.CoverImageURL, recordHits.Load(), cover)
	}
	if _, err := resolver.ResolveCoverURL(context.Background(), "https://comick.dev/comic/x"); err == nil {
		t.Fatal("expected another site's URL to be refused")
	}
}
