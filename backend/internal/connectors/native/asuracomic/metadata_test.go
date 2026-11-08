package asuracomic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// asuraMetadataSeriesPage is a series page cut down from the live markup
// (doctors-rebirth, 2026-11): the Article and ComicSeries JSON-LD blocks with
// their truncated description, the Status and Type boxes, the creator rows,
// the genre chips and the full synopsis block.
const asuraMetadataSeriesPage = `<!DOCTYPE html><html lang="en" class="dark"> <head><meta charset="UTF-8">
<title>Doctor’s Rebirth | Asura Scans</title><meta name="description" content="I had been abroad to do medical volunteer work, when I was swept up in a civil war. I was killed by a rebel soldier while taking a bullet for a patient. But when I finally woke up, I had become a child?!?And in front of me stood a destroyed carriage and a group of oddly dressed people, who were unco...">
<meta property="og:title" content="Doctor’s Rebirth | Asura Scans"><meta property="og:description" content="I had been abroad to do medical volunteer work, when I was swept up in a civil war. I was killed by a rebel soldier while taking a bullet for a patient. But when I finally woke up, I had become a child?!?And in front of me stood a destroyed carriage and a group of oddly dressed people, who were unco...">
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Organization","name":"Asura Scans","url":"https://asurascans.com"}</script>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Article","headline":"Doctor’s Rebirth","description":"I had been abroad to do medical volunteer work...","author":{"@type":"Person","name":"Article Author"},"articleSection":"Action, Adventure, Fantasy, Isekai, Murim"}</script>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"ComicSeries","name":"Doctor’s Rebirth","alternateName":"Our Rebirth • Doctor Rebirth","description":"I had been abroad to do medical volunteer work, when I was swept up in a civil war. I was killed by a rebel soldier while taking a bullet for a patient. But when I finally woke up, I had become a child?!?And in front of me stood a destroyed carriage and a group of oddly dressed people, who were unco...","url":"https://asurascans.com/comics/doctors-rebirth-ab39de10","genre":["Action","Adventure","Fantasy","Isekai","Murim"],"author":{"@type":"Person","name":"Taesun"},"illustrator":{"@type":"Person","name":"Dobin"},"numberOfEpisodes":250,"aggregateRating":{"@type":"AggregateRating","ratingValue":"8.9","bestRating":"10","worstRating":"1","ratingCount":39168}}</script>
</head><body>
<div class="mt-3 relative"> <div id="description-text" class="text-sm lg:text-base font-light text-white/80 leading-relaxed prose prose-invert max-w-full line-clamp-3 lg:cursor-pointer"><p>I had been abroad to do medical volunteer work, when I was swept up in a civil war. I was killed by a rebel soldier while taking a bullet for a patient. But when I finally woke up, I had become a child?!?And in front of me stood a destroyed carriage and a group of oddly dressed people, who were unconscious!"It's just like a Murim world."</p><p>This is the story of how I became the greatest physician in Gangho!</p></div> </div>
<div class="flex gap-3 pt-4 border-t border-white/10"> <div class="flex-1 bg-[#1C1924] rounded px-4 py-3"> <div class="text-xs text-white/50 mb-1">Status</div> <div class="flex items-center gap-2"> <span class="w-2.5 h-2.5 rounded-full bg-[#A78BFA]"></span> <span class="text-base font-bold text-[#A78BFA] capitalize"> ongoing </span> </div> </div> <div class="flex-1 bg-[#1C1924] rounded px-4 py-3"> <div class="text-xs text-white/50 mb-1">Type</div> <div class="flex items-center gap-2"> <span class="w-2.5 h-2.5 rounded-full bg-[#913FE2]"></span> <span class="text-base font-bold text-[#913FE2] uppercase"> manhwa </span> </div> </div> </div>
<div class="flex items-center gap-2"> <span class="text-xs text-white/50">Author</span> </div> <a href="/browse?author=Taesun" class="text-sm font-medium hover:text-[#913FE2] transition-colors">Taesun</a>
<div class="flex items-center gap-2"> <span class="text-xs text-white/50">Artist</span> </div> <a href="/browse?artist=Dobin" class="text-sm font-medium hover:text-[#913FE2] transition-colors">Dobin</a>
<div class="hidden lg:flex max-w-full gap-2 flex-wrap"> <a href="/browse?genres=action" class="inline-flex flex-shrink-0 text-xs font-medium px-3 py-1.5"> Action </a><a href="/browse?genres=adventure" class="inline-flex flex-shrink-0 text-xs font-medium px-3 py-1.5"> Adventure </a></div>
<a href="/comics/doctors-rebirth-ab39de10/chapter/249">Chapter 249</a>
<a href="/comics/doctors-rebirth-ab39de10/chapter/250">Chapter 250</a>
</body></html>`

func newAsuraMetadataServer(t *testing.T, pages map[string]string) *Connector {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/comics/doctors-rebirth-ab39de10">Doctor's Rebirth</a></body></html>`))
	})
	for path, page := range pages {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(page))
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
}

// TestAsuraComicResolveFillsMetadataFromTheSeriesPage pins where each field
// comes from: genres, creators and the score from the ComicSeries JSON-LD
// (not the Article block beside it), status and type from the labelled boxes,
// and the synopsis from the full block rather than the copies cut at "...".
// Search reads the same page but leaves the metadata to the resolve.
func TestAsuraComicResolveFillsMetadataFromTheSeriesPage(t *testing.T) {
	conn := newAsuraMetadataServer(t, map[string]string{"/comics/doctors-rebirth-ab39de10": asuraMetadataSeriesPage})

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/doctors-rebirth-ab39de10")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
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
	if want := []string{"Action", "Adventure", "Fantasy", "Isekai", "Murim"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"Taesun"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"Dobin"}; !reflect.DeepEqual(meta.Artists, want) {
		t.Errorf("artists: got %v, want %v", meta.Artists, want)
	}
	if meta.Rating == nil || *meta.Rating != 8.9 {
		t.Errorf("rating: got %v, want 8.9", meta.Rating)
	}
	if meta.RatingVotes != 39168 {
		t.Errorf("rating votes: got %d", meta.RatingVotes)
	}
	wantDescription := "I had been abroad to do medical volunteer work, when I was swept up in a civil war. I was killed by a rebel soldier while taking a bullet for a patient. But when I finally woke up, I had become a child?!?And in front of me stood a destroyed carriage and a group of oddly dressed people, who were unconscious!\"It's just like a Murim world.\"\n\nThis is the story of how I became the greatest physician in Gangho!"
	if meta.Description != wantDescription {
		t.Errorf("description: got %q, want %q", meta.Description, wantDescription)
	}
	if meta.Year != 0 || meta.ContentRating != "" || len(meta.Tags) != 0 {
		t.Errorf("expected no year, content rating or tags, got %+v", meta)
	}

	results, err := conn.SearchByTitle(context.Background(), "doctor", 5)
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

// TestAsuraComicMetadataToleratesOtherJSONLDShapes pins the leniency of the
// JSON-LD reading: the score as numbers, an illustrator list, genre as one
// comma-separated string, the block inside an @graph, collaborators packed
// into one author field, and only cut synopsis copies (the longest wins).
// Without the boxes, status and type stay empty.
func TestAsuraComicMetadataToleratesOtherJSONLDShapes(t *testing.T) {
	page := `<html><head>
<meta property="og:description" content="A short cut copy...">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},{"@type":["ComicSeries","CreativeWork"],"genre":"Action, Martial Arts","author":{"@type":"Person","name":"AZI,COBY /Emperor Penguin"},"illustrator":[{"@type":"Person","name":"Lee Je-Won"},{"@type":"Person","name":"Studio Two"}],"description":"Jin Runcandel was the youngest son of Runcandel, the land's most prestigious swordsman family...","aggregateRating":{"@type":"AggregateRating","ratingValue":4.75,"bestRating":5,"ratingCount":"1,204"}}]}</script>
</head><body><a href="/comics/swordmasters-youngest-son-ab39de10/chapter/220">Chapter 220</a></body></html>`
	conn := newAsuraMetadataServer(t, map[string]string{"/comics/swordmasters-youngest-son-ab39de10": page})

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/swordmasters-youngest-son-ab39de10")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata from the JSON-LD block")
	}
	if want := []string{"Action", "Martial Arts"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"AZI", "COBY", "Emperor Penguin"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"Lee Je-Won", "Studio Two"}; !reflect.DeepEqual(meta.Artists, want) {
		t.Errorf("artists: got %v, want %v", meta.Artists, want)
	}
	if meta.Rating == nil || *meta.Rating != 9.5 {
		t.Errorf("rating: got %v, want 9.5 (4.75 of 5)", meta.Rating)
	}
	if meta.RatingVotes != 1204 {
		t.Errorf("rating votes: got %d", meta.RatingVotes)
	}
	if meta.Description != "Jin Runcandel was the youngest son of Runcandel, the land's most prestigious swordsman family..." {
		t.Errorf("description: got %q", meta.Description)
	}
	if meta.Type != "" || meta.Status != "" {
		t.Errorf("expected no type or status without the boxes, got %q / %q", meta.Type, meta.Status)
	}
}

// TestAsuraComicMetadataFallsBackToTheRenderedLinks pins that the genre chips
// and creator rows stand in when the page carries no ComicSeries block.
func TestAsuraComicMetadataFallsBackToTheRenderedLinks(t *testing.T) {
	page := `<html><body>
<a href="/browse?author=Black%20Ajin" class="text-sm">Black Ajin</a>
<a href="/browse?artist=Ant%20Studio" class="text-sm">Ant Studio</a>
<div class="hidden lg:flex"> <a href="/browse?genres=action" class="inline-flex"> Action </a><a href="/browse?genres=murim" class="inline-flex"> Murim </a></div>
<a href="/comics/the-hero-returns-ab39de10/chapter/178">Chapter 178</a>
</body></html>`
	conn := newAsuraMetadataServer(t, map[string]string{"/comics/the-hero-returns-ab39de10": page})

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/the-hero-returns-ab39de10")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata from the rendered links")
	}
	if want := []string{"Action", "Murim"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"Black Ajin"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"Ant Studio"}; !reflect.DeepEqual(meta.Artists, want) {
		t.Errorf("artists: got %v, want %v", meta.Artists, want)
	}
}

// TestAsuraComicStatusBoxWording pins the Status box mapping, including
// Asura's own "Season End" for a series paused between seasons.
func TestAsuraComicStatusBoxWording(t *testing.T) {
	cases := map[string]string{
		"ongoing":     connectors.SeriesStatusOngoing,
		"Completed":   connectors.SeriesStatusCompleted,
		"hiatus":      connectors.SeriesStatusHiatus,
		"Season End":  connectors.SeriesStatusHiatus,
		"dropped":     connectors.SeriesStatusCancelled,
		"coming soon": "",
	}
	for raw, want := range cases {
		if got := normalizeStatus(raw); got != want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestAsuraComicGarbledMetadataNeverFailsTheResolve pins that metadata is a
// bonus: a JSON-LD block cut off mid-object, a score out of range and boxes
// holding words outside the vocabulary leave the metadata nil while the
// resolve still answers with its chapter.
func TestAsuraComicGarbledMetadataNeverFailsTheResolve(t *testing.T) {
	page := `<html><head>
<meta property="og:title" content="Nano Machine - Asura Scans">
<script type="application/ld+json">{"@context":"https://schema.org","@type":"ComicSeries","genre":["Action",</script>
<script type="application/ld+json">{"@type":"ComicSeries","aggregateRating":{"ratingValue":"11","bestRating":"10","ratingCount":12}}</script>
</head><body>
<div class="text-xs text-white/50 mb-1">Status</div> <div class="flex"> <span class="capitalize"> coming soon </span> </div>
<div class="text-xs text-white/50 mb-1">Type</div> <div class="flex"> <span class="uppercase"> webtoon </span> </div>
<a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body></html>`
	conn := newAsuraMetadataServer(t, map[string]string{"/comics/nano-machine-7f873ca6": page})

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("garbled metadata must not fail the resolve: %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 304 {
		t.Fatalf("expected latest chapter 304, got %v", resolved.LatestChapter)
	}
	if resolved.Metadata != nil {
		t.Fatalf("expected no metadata from a garbled page, got %+v", resolved.Metadata)
	}
}
