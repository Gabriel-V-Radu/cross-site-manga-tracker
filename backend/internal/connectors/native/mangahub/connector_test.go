package mangahub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestConnector(t *testing.T, handler http.Handler) *Connector {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewConnectorWithOptions(
		"https://mangahub.io",
		server.URL+"/graphql",
		[]string{"mangahub.io"},
		&http.Client{Timeout: 5 * time.Second},
	)
}

// graphqlHandler routes on the query text, the way the real endpoint does.
func graphqlHandler(t *testing.T, respond func(query string) string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("malformed request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respond(payload.Query)))
	})
}

const onePieceManga = `{"id":20,"title":"One Piece","slug":"one-piece_142","image":"wbctr/one-piece.jpg",
	"latestChapter":1191,"updatedDate":"2026-08-21T03:53:28.000Z",
	"alternativeTitle":"ワンピース; One Piece; Vua Hải Tặc"}`

func onePieceHandler(t *testing.T) http.Handler {
	t.Helper()
	return graphqlHandler(t, func(query string) string {
		switch {
		case strings.HasPrefix(query, "{search("):
			return `{"data":{"search":{"rows":[` + onePieceManga + `,{"id":0,"title":"Broken","slug":""}]}}}`
		case strings.Contains(query, `slug:"one-piece_142"`):
			return `{"data":{"manga":` + onePieceManga + `}}`
		default:
			return `{"errors":[{"message":"Cannot read properties of undefined (reading 'mangaID')"}],"data":{"manga":null}}`
		}
	})
}

func TestResolveByURLReadsChapterDateAndTitles(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))

	result, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/manga/one-piece_142")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.Title != "One Piece" {
		t.Fatalf("unexpected title %q", result.Title)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 1191 {
		t.Fatalf("expected latest chapter 1191, got %#v", result.LatestChapter)
	}
	want := time.Date(2026, 8, 21, 3, 53, 28, 0, time.UTC)
	if result.LastUpdatedAt == nil || !result.LastUpdatedAt.Equal(want) {
		t.Fatalf("expected update time %v, got %v", want, result.LastUpdatedAt)
	}
	if result.SourceItemID != "20" {
		t.Fatalf("unexpected source item id %q", result.SourceItemID)
	}
	if result.URL != "https://mangahub.io/manga/one-piece_142" {
		t.Fatalf("unexpected canonical url %q", result.URL)
	}
	if result.CoverImageURL != "https://thumb.mghcdn.com/wbctr/one-piece.jpg" {
		t.Fatalf("unexpected cover %q", result.CoverImageURL)
	}
	// The main title is dropped from alternatives; the rest survive.
	if len(result.RelatedTitles) != 2 || result.RelatedTitles[0] != "ワンピース" || result.RelatedTitles[1] != "Vua Hải Tặc" {
		t.Fatalf("unexpected related titles %v", result.RelatedTitles)
	}
}

func TestResolveByURLAcceptsChapterURLs(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))

	result, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/chapter/one-piece_142/chapter-1100")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LatestChapter == nil || *result.LatestChapter != 1191 {
		t.Fatalf("expected latest chapter 1191, got %#v", result.LatestChapter)
	}
}

func TestResolveByURLRejectsForeignHosts(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))
	if _, err := connector.ResolveByURL(context.Background(), "https://mangadex.org/title/x"); err == nil {
		t.Fatalf("expected foreign host to be rejected")
	}
}

func TestResolveByURLSurfacesGraphQLErrors(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))
	_, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/manga/no-such-slug")
	if err == nil || !strings.Contains(err.Error(), "graphql error") {
		t.Fatalf("expected the graphql error to surface, got %v", err)
	}
}

func TestSearchByTitleMapsRowsAndDropsSluglessOnes(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))

	results, err := connector.SearchByTitle(context.Background(), "one piece", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the slugless row to be dropped, got %d results", len(results))
	}
	if results[0].URL != "https://mangahub.io/manga/one-piece_142" {
		t.Fatalf("unexpected url %q", results[0].URL)
	}
	// MangaHub's catalog is English-only, so unlike ComicK its search rows may
	// carry their chapter numbers: they are the same value a resolve returns.
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 1191 {
		t.Fatalf("expected search row to carry chapter 1191, got %#v", results[0].LatestChapter)
	}
}

func TestResolveChapterURLChecksRangeAndBuildsReaderLink(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))

	url, err := connector.ResolveChapterURL(context.Background(), "https://mangahub.io/manga/one-piece_142", 1100)
	if err != nil {
		t.Fatalf("resolve chapter failed: %v", err)
	}
	if url != "https://mangahub.io/chapter/one-piece_142/chapter-1100" {
		t.Fatalf("unexpected chapter url %q", url)
	}

	// Decimal numbers survive into the URL; verified live against the reader.
	decimalURL, err := connector.ResolveChapterURL(context.Background(), "https://mangahub.io/manga/one-piece_142", 527.6)
	if err != nil {
		t.Fatalf("resolve decimal chapter failed: %v", err)
	}
	if decimalURL != "https://mangahub.io/chapter/one-piece_142/chapter-527.6" {
		t.Fatalf("unexpected decimal chapter url %q", decimalURL)
	}

	if _, err := connector.ResolveChapterURL(context.Background(), "https://mangahub.io/manga/one-piece_142", 1500); err == nil {
		t.Fatalf("expected a chapter beyond the latest to be refused")
	}
}

func TestResolveByURLSkipsImplausibleChapterNumbers(t *testing.T) {
	connector := newTestConnector(t, graphqlHandler(t, func(string) string {
		return `{"data":{"manga":{"id":9,"title":"Corrupt","slug":"corrupt_1","latestChapter":13521,
			"updatedDate":"2026-08-01T00:00:00.000Z"}}}`
	}))

	result, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/manga/corrupt_1")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if result.LatestChapter != nil || result.LastUpdatedAt != nil {
		t.Fatalf("expected corrupt numbers to be dropped, got %#v %#v", result.LatestChapter, result.LastUpdatedAt)
	}
}
