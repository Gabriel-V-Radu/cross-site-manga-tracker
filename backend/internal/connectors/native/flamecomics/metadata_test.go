package flamecomics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// flameMetadataSeriesPage is a series page cut down from the live markup
// (series 153, 2026-11): the CreativeWorkSeries JSON-LD block, the rendered
// chapter rows the chapter scrape reads, and the __NEXT_DATA__ payload with
// the series record and the head of its chapter list.
const flameMetadataSeriesPage = `<!DOCTYPE html><html lang="en"><head>
<meta property="og:title" content="Return of The Frozen Player - Flame Comics">
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Organization","name":"Flame Comics","url":"https://flamecomics.xyz","description":"We are a scanlation group, that dedicates itself to creating quality English translations for Mangas, Manhwas, and Manhuas!"}</script>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"CreativeWorkSeries","name":"Return of The Frozen Player","alternateName":["The Frozen Player Returns"],"description":"5 years after the world changed, the final boss appeared.","url":"https://flamecomics.xyz/series/153","inLanguage":"English","genre":["Action","Fantasy","Adventure","Shounen"],"identifier":"153","author":{"@type":"Person","name":"JerryM"},"illustrator":[{"@type":"Person","name":"SilSil"},{"@type":"Person","name":"Studio Inus"}],"datePublished":"2021-01-01","isFamilyFriendly":true}</script>
</head><body>
<a href="/series/153/870260a0ea7c15d4"><p>Chapter 233</p><p>November 6, 2026 6:06 PM</p></a>
<a href="/series/153/7166c6c7cb6ce398"><p>Chapter 232</p><p>October 30, 2026 3:10 PM</p></a>
<script id="__NEXT_DATA__" type="application/json">{"props":{"__N_SSG":true,"pageProps":{"series":{"series_id":153,"title":"Return of The Frozen Player","altTitles":["The Frozen Player Returns"],"description":"<p class=\"mantine-focus-auto m_b6d8b162 mantine-Text-root\">5 years after the world changed, the final boss appeared. [The final boss for area Earth, the Frost Queen, has appeared.]</p><p>And then 25 years passed… &ldquo;A second floor?&rdquo;</p>","language":"English","type":"Manhwa","tags":["Action","Fantasy","Adventure","Shounen"],"country":"KR","author":["JerryM"],"artist":["SilSil","Studio Inus"],"publisher":["Kakao","CarroToon"],"year":2021,"status":"Ongoing","schedule":"Weekly","likes":447,"cover":"thumbnail.jpeg","draft":0,"official":"","novel_id":null,"last_edit":1774616895,"time":1746749212},"chapters":[{"chapter_id":12406,"series_id":153,"chapter":"233.00","title":"","release_date":1793988383,"token":"870260a0ea7c15d4"}]},"__N_SSG":true},"page":"/series/[id]","query":{"id":"153"},"buildId":"abc","isFallback":false,"gsp":true}</script>
</body></html>`

func newFlameMetadataConnector(t *testing.T, pages map[string]string) *Connector {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/series/153">Return of The Frozen Player</a></body></html>`))
	})
	for path, page := range pages {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(page))
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewConnectorWithOptions(server.URL, []string{"flamecomics.xyz"}, &http.Client{Timeout: 5 * time.Second})
}

// TestFlameComicsResolveFillsMetadataFromTheSeriesRecord pins the mapping of
// the __NEXT_DATA__ series record: type, status and year as normalized values,
// tags kept whole as genres (demographics included), creators as listed and
// the HTML synopsis as plain paragraphs. Search reads the same page but leaves
// the metadata to the resolve.
func TestFlameComicsResolveFillsMetadataFromTheSeriesRecord(t *testing.T) {
	conn := newFlameMetadataConnector(t, map[string]string{"/series/153": flameMetadataSeriesPage})

	resolved, err := conn.ResolveByURL(context.Background(), "https://flamecomics.xyz/series/153")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 233 {
		t.Fatalf("expected latest chapter 233, got %v", resolved.LatestChapter)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata from the series page")
	}
	if meta.Type != connectors.SeriesTypeManhwa {
		t.Errorf("type: got %q", meta.Type)
	}
	if meta.Status != connectors.SeriesStatusOngoing {
		t.Errorf("status: got %q", meta.Status)
	}
	if meta.Year != 2021 {
		t.Errorf("year: got %d", meta.Year)
	}
	if want := []string{"Action", "Fantasy", "Adventure", "Shounen"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"JerryM"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"SilSil", "Studio Inus"}; !reflect.DeepEqual(meta.Artists, want) {
		t.Errorf("artists: got %v, want %v", meta.Artists, want)
	}
	wantDescription := "5 years after the world changed, the final boss appeared. [The final boss for area Earth, the Frost Queen, has appeared.]\n\nAnd then 25 years passed… “A second floor?”"
	if meta.Description != wantDescription {
		t.Errorf("description: got %q, want %q", meta.Description, wantDescription)
	}
	if meta.Rating != nil || meta.ContentRating != "" || len(meta.Tags) != 0 {
		t.Errorf("expected no rating, content rating or tags, got %+v", meta)
	}

	results, err := conn.SearchByTitle(context.Background(), "frozen player", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one search result, got %d", len(results))
	}
	if results[0].Metadata != nil {
		t.Fatalf("search results must leave metadata to the resolve, got %+v", results[0].Metadata)
	}
}

// TestFlameComicsMetadataFallsBackToCountryAndJSONLD pins the two fallbacks:
// a type label outside the vocabulary gives way to the country code, and the
// fields the record lacks (here creators and synopsis) come from the JSON-LD
// series block. Fields the record does carry win over the JSON-LD.
func TestFlameComicsMetadataFallsBackToCountryAndJSONLD(t *testing.T) {
	page := `<html><head>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"CreativeWorkSeries","genre":["Should","Not","Win"],"author":{"@type":"Person","name":"Sing-Shong"},"illustrator":[{"@type":"Person","name":"Sleepy-C"},{"@type":"Person","name":"Redice Studio"}],"description":"‘This is a development that I know of.’","datePublished":"2018-01-01"}</script>
</head><body>
<a href="/series/2/abcdef1234567890"><p>Chapter 300</p></a>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"series":{"series_id":2,"type":"Webtoon","country":"CN","tags":"Action, Survival","year":"2020","status":"Hiatus"}}}}</script>
</body></html>`
	conn := newFlameMetadataConnector(t, map[string]string{"/series/2": page})

	resolved, err := conn.ResolveByURL(context.Background(), "https://flamecomics.xyz/series/2")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata")
	}
	if meta.Type != connectors.SeriesTypeManhua {
		t.Errorf("type: got %q, want manhua from the country code", meta.Type)
	}
	if meta.Status != connectors.SeriesStatusHiatus {
		t.Errorf("status: got %q", meta.Status)
	}
	if meta.Year != 2020 {
		t.Errorf("year: got %d, want the record's 2020 over the JSON-LD date", meta.Year)
	}
	if want := []string{"Action", "Survival"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"Sing-Shong"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"Sleepy-C", "Redice Studio"}; !reflect.DeepEqual(meta.Artists, want) {
		t.Errorf("artists: got %v, want %v", meta.Artists, want)
	}
	if meta.Description != "‘This is a development that I know of.’" {
		t.Errorf("description: got %q", meta.Description)
	}
}

// TestFlameComicsMetadataFromJSONLDAlone pins that the JSON-LD block carries
// the metadata on its own when the page has no __NEXT_DATA__, as it would
// after a move to the app router. Type and status are only in the record, so
// they stay empty.
func TestFlameComicsMetadataFromJSONLDAlone(t *testing.T) {
	page := `<html><head>
<script type="application/ld+json">[{"@type":"BreadcrumbList"},{"@type":"ComicSeries","genre":"Action, Revenge","author":"Chungmin","illustrator":{"@type":"Person","name":"Lee Soo-Hyun"},"description":"<p>'Cha Joo-Young' is imprisoned in a juvenile prison.</p>","datePublished":"2024-01-01"}]</script>
</head><body><a href="/series/140/abcdef1234567890"><p>Chapter 90</p></a></body></html>`
	conn := newFlameMetadataConnector(t, map[string]string{"/series/140": page})

	resolved, err := conn.ResolveByURL(context.Background(), "https://flamecomics.xyz/series/140")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata from the JSON-LD block")
	}
	if want := []string{"Action", "Revenge"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"Chungmin"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"Lee Soo-Hyun"}; !reflect.DeepEqual(meta.Artists, want) {
		t.Errorf("artists: got %v, want %v", meta.Artists, want)
	}
	if meta.Year != 2024 {
		t.Errorf("year: got %d", meta.Year)
	}
	if meta.Description != "'Cha Joo-Young' is imprisoned in a juvenile prison." {
		t.Errorf("description: got %q", meta.Description)
	}
	if meta.Type != "" || meta.Status != "" {
		t.Errorf("expected no type or status without the record, got %q / %q", meta.Type, meta.Status)
	}
}

// TestFlameComicsGarbledMetadataNeverFailsTheResolve pins that metadata is a
// bonus: a __NEXT_DATA__ payload cut off mid-object, a JSON-LD block that is
// not JSON and a series record of the wrong shape leave the metadata nil while
// the resolve still answers with its chapter.
func TestFlameComicsGarbledMetadataNeverFailsTheResolve(t *testing.T) {
	pages := map[string]string{
		"/series/7": `<html><head><script type="application/ld+json">{"@type":"CreativeWorkSeries","genre":[</script></head><body>
<a href="/series/7/abcdef1234567890"><p>Chapter 12</p></a>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"series":{"type":"Manhwa","tags":["Act</script>
</body></html>`,
		"/series/8": `<html><body>
<a href="/series/8/abcdef1234567890"><p>Chapter 12</p></a>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"series":["not","an","object"]}}}</script>
</body></html>`,
		"/series/9": `<html><body>
<a href="/series/9/abcdef1234567890"><p>Chapter 12</p></a>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"series":{"type":{"name":"?"},"status":12,"year":"soon","tags":[{"id":1}],"author":null}}}}</script>
</body></html>`,
	}
	conn := newFlameMetadataConnector(t, pages)

	for _, id := range []string{"7", "8", "9"} {
		resolved, err := conn.ResolveByURL(context.Background(), "https://flamecomics.xyz/series/"+id)
		if err != nil {
			t.Fatalf("series %s: garbled metadata must not fail the resolve: %v", id, err)
		}
		if resolved.LatestChapter == nil || *resolved.LatestChapter != 12 {
			t.Fatalf("series %s: expected latest chapter 12, got %v", id, resolved.LatestChapter)
		}
		if resolved.Metadata != nil {
			t.Fatalf("series %s: expected no metadata from a garbled page, got %+v", id, resolved.Metadata)
		}
	}
}
