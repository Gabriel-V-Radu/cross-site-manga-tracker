package mangafire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A resolve reads the detail record once a day and the English chapter
// listing every time: the record describes the series, the listing is what
// moves between polls.
func TestMangaFireResolveCachesTheDetailRecord(t *testing.T) {
	var detailHits, chapterHits atomic.Int32
	chapter := atomic.Int32{}
	chapter.Store(1187)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		detailHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"dkw","slug":"one-piece","title":"One Piece","type":"manga"}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		chapterHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":75117,"number":` + strconv.Itoa(int(chapter.Load())) + `,"language":"en","createdAt":1783047602}],"meta":{"hasNext":false}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	for poll := 0; poll < 3; poll++ {
		if poll == 2 {
			chapter.Store(1188)
		}
		resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
		if err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		if resolved.Title != "One Piece" || resolved.Metadata == nil || resolved.Metadata.Type != "manga" {
			t.Fatalf("poll %d: the cached record must still give title and metadata, got %+v", poll, resolved)
		}
		if want := 1187.0 + float64(poll/2); resolved.LatestChapter == nil || *resolved.LatestChapter != want {
			t.Fatalf("poll %d: chapter %v, want %v", poll, resolved.LatestChapter, want)
		}
	}
	if detailHits.Load() != 1 || chapterHits.Load() != 3 {
		t.Fatalf("detail asked %d times, chapters %d; want 1 and 3", detailHits.Load(), chapterHits.Load())
	}
}

// A cached record whose chapters answer 404 is asked for again: the title may
// have moved, and a stale record must not keep failing the poll for a day.
func TestMangaFireStaleCachedRecordHeals(t *testing.T) {
	var detailHits atomic.Int32
	var gone atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/dkw", func(w http.ResponseWriter, _ *http.Request) {
		detailHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		hid := "dkw"
		if gone.Load() {
			hid = "dkx"
		}
		_, _ = w.Write([]byte(`{"data":{"id":1,"hid":"` + hid + `","slug":"one-piece","title":"One Piece"}}`))
	})
	mux.HandleFunc("/api/titles/dkw/chapters", func(w http.ResponseWriter, _ *http.Request) {
		if gone.Load() {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":1,"number":10,"language":"en","createdAt":1783047602}],"meta":{"hasNext":false}}`))
	})
	mux.HandleFunc("/api/titles/dkx/chapters", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":2,"number":11,"language":"en","createdAt":1783047602}],"meta":{"hasNext":false}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	if _, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece"); err != nil {
		t.Fatal(err)
	}
	gone.Store(true)
	resolved, err := connector.ResolveByURL(context.Background(), "https://mangafire.to/title/dkw-one-piece")
	if err != nil {
		t.Fatalf("a stale cached record must heal, got %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 11 || detailHits.Load() != 2 {
		t.Fatalf("chapter %v after %d detail requests; want 11 after 2", resolved.LatestChapter, detailHits.Load())
	}
}
