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

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
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
	"alternativeTitle":"ワンピース; One Piece; Vua Hải Tặc; One Piece Digital Colored"}`

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
	// The main title and the non-Latin alternates are dropped (the dashboard
	// search and the link scan only ever compare English spellings); the
	// English alternate survives.
	if len(result.RelatedTitles) != 1 || result.RelatedTitles[0] != "One Piece Digital Colored" {
		t.Fatalf("unexpected related titles %v", result.RelatedTitles)
	}
}

// A clean answer with a null manga record means the series does not exist
// under that slug; it has to read as a 404 the way a scraper's missing page
// does, so the poller can tell "gone" from "did not answer".
func TestResolveByURLNullMangaIsNotFound(t *testing.T) {
	connector := newTestConnector(t, graphqlHandler(t, func(string) string {
		return `{"data":{"manga":null}}`
	}))

	_, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/manga/no-such-slug")
	if !connectors.IsNotFound(err) {
		t.Fatalf("expected a not-found verdict, got %v", err)
	}
	if _, err := connector.ResolveChapterURL(context.Background(), "https://mangahub.io/manga/no-such-slug", 1); !connectors.IsNotFound(err) {
		t.Fatalf("expected a not-found verdict from the chapter lookup, got %v", err)
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

// A quota refusal arrives as HTTP 200 with an errors array, which no
// status-code classifier can see; the connector has to turn it into a typed
// 429 itself or the poller treats a rate-limit episode as a normal failure.
func TestRateLimitErrorBecomesTyped429(t *testing.T) {
	connector := newTestConnector(t, graphqlHandler(t, func(string) string {
		return `{"errors":[{"message":"API rate limit excessed"}],"data":{"manga":null}}`
	}))

	_, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/manga/one-piece_142")
	if err == nil {
		t.Fatalf("expected the rate limit to surface as an error")
	}
	if !connectors.IsHTTPStatus(err, http.StatusTooManyRequests) {
		t.Fatalf("expected a typed 429 verdict, got %v", err)
	}
	if !strings.Contains(err.Error(), "rate limit excessed") {
		t.Fatalf("expected the site's own wording to survive, got %v", err)
	}
}

// A schema error shares the same envelope but says nothing about quota, so it
// must not be dressed up as a rate limit.
func TestSchemaErrorIsNotClassifiedAsRateLimit(t *testing.T) {
	connector := newTestConnector(t, onePieceHandler(t))

	_, err := connector.ResolveByURL(context.Background(), "https://mangahub.io/manga/no-such-slug")
	if err == nil {
		t.Fatalf("expected the graphql error to surface")
	}
	if connectors.IsHTTPStatus(err, http.StatusTooManyRequests) {
		t.Fatalf("expected no rate-limit verdict, got %v", err)
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

// The registry maps hosts solely through SiteInfo now, and the switch it
// replaced routed api.mghcdn.com here too. The API host is on a different
// registrable domain than mangahub.io, so nothing covers it implicitly: it has
// to be claimed by name or those lookups resolve to nothing.
func TestHostsClaimsAPIOriginWithoutWideningSeriesURLs(t *testing.T) {
	connector := NewConnector()

	if !connectors.HostAllowed("api.mghcdn.com", connector.Hosts()) {
		t.Fatalf("expected the API host to be claimed, got %v", connector.Hosts())
	}
	if !connectors.HostAllowed("mangahub.io", connector.Hosts()) {
		t.Fatalf("expected the site host to be claimed, got %v", connector.Hosts())
	}

	// Claiming it for routing must not make it a series URL: the API serves no
	// series pages, so a slug read out of one would be fiction.
	if _, err := connector.parseSeriesURL("https://api.mghcdn.com/manga/one-piece_142"); err == nil {
		t.Fatalf("expected the API host to be refused as a series URL")
	}

	// Hosts() must not hand out the shared default slice itself.
	claimed := connector.Hosts()
	claimed[0] = "tampered.example"
	if site.SiteHosts[0] != "mangahub.io" {
		t.Fatalf("Hosts() aliased the package default: %v", site.SiteHosts)
	}
}

// The API's search matches loosely, so its rows are post-filtered against the
// query the way every other connector's are: a row whose title does not answer
// the query is dropped, however the site ranked it.
func TestSearchByTitleDropsRowsThatDoNotMatchTheQuery(t *testing.T) {
	connector := newTestConnector(t, graphqlHandler(t, func(string) string {
		return `{"data":{"search":{"rows":[` + onePieceManga + `,
			{"id":7,"title":"Bleach","slug":"bleach_1","latestChapter":700}]}}}`
	}))

	results, err := connector.SearchByTitle(context.Background(), "one piece", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Title != "One Piece" {
		t.Fatalf("expected only the matching row, got %+v", results)
	}
}
