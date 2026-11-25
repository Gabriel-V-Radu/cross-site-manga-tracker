package mangafire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// crossLinksServer answers one title record with the given links field and
// counts the requests it takes.
func crossLinksServer(t *testing.T, links string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/titles/jv30n", func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":18696,"hid":"jv30n","slug":"teasing-master-takagi-san-official-colored",
			"title":"Teasing Master Takagi-san (Official Colored)","links":` + links + `,"malId":"78537"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &requests
}

// The record's links come back under the keys the link scan uses, only for
// the sites something here reads: AniList and MyAnimeList are dropped.
func TestCrossLinksMapsTheRecordsLinks(t *testing.T) {
	server, requests := crossLinksServer(t, `{
		"mal":"https://myanimelist.net/manga/78537/",
		"al":"https://anilist.co/manga/85533/",
		"mu":"https://mangaupdates.com/series/3kqrqxe/",
		"md":"https://mangadex.org/title/db0c471e-d88b-4f71-b35a-dc39f1ea8c15/",
		"mb":"https://mangabaka.org/6957/"}`)
	connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})

	links, err := connector.CrossLinks(context.Background(), "https://mangafire.to/title/jv30n-teasing-master-takagi-san-official-colored")
	if err != nil {
		t.Fatalf("cross links: %v", err)
	}
	want := map[string]string{
		"mangaupdates": "https://mangaupdates.com/series/3kqrqxe/",
		"mangadex":     "https://mangadex.org/title/db0c471e-d88b-4f71-b35a-dc39f1ea8c15/",
		"mangabaka":    "https://mangabaka.org/6957/",
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("links = %v, want %v", links, want)
	}

	// The record is the one resolves cache: asking again costs nothing.
	if _, err := connector.CrossLinks(context.Background(), "https://mangafire.to/title/jv30n"); err != nil {
		t.Fatalf("second ask: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("two asks cost %d requests, want 1 (the cached record)", got)
	}
}

// A title without links sends "links":[] instead of an object; that and any
// other odd shape mean "no links", never a failed record.
func TestCrossLinksToleratesOddShapes(t *testing.T) {
	for name, links := range map[string]string{
		"empty array":      `[]`,
		"null":             `null`,
		"not a url":        `{"md":"mangadex db0c471e","mu":42,"mb":""}`,
		"relative and ftp": `{"md":"/title/db0c471e","mu":"ftp://mangaupdates.com/series/3kqrqxe"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := crossLinksServer(t, links)
			connector := NewConnectorWithOptions(server.URL, []string{"mangafire.to"}, &http.Client{Timeout: 5 * time.Second})
			got, err := connector.CrossLinks(context.Background(), "https://mangafire.to/title/jv30n")
			if err != nil {
				t.Fatalf("an odd links field must not fail the record: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("links = %v, want none", got)
			}
		})
	}
}
