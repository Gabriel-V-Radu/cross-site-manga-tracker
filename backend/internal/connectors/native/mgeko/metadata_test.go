package mgeko

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// mgekoMetadataMangaPage is a manga page cut down from the live markup
// (tonari-no-seki-no-yatsu-ga-soiu-me-de-mitekuru, 2026-11): the templated
// meta descriptions, the "updating" author placeholder, the rating microdata,
// the header stats, the categories with their stray leading spaces, the About
// text with its generated blurb before the synopsis, and the tag pills.
const mgekoMetadataMangaPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta name="title" content="Tonari no Seki no Yatsu ga Souiu Me de Mitekuru [ALL CHAPTERS]">
<meta name="description" content="Tonari no Seki no Yatsu ga Souiu Me de Mitekuru is a manga/manhwa/manhua written by updating and is updated fast and free on www.mgeko.cc. Enjoy reading online on Mgeko." />
<meta property="og:description" content="Tonari no Seki no Yatsu ga Souiu Me de Mitekuru is a manga/manhwa/manhua written by updating and is updated fast and free on www.mgeko.cc.Enjoy reading Online on Mgeko." />
<style>
        .categories li{
          background-color: #3319E8 !important;
        }
        .description {
          border: 1px dashed #00AEF0 !important;
        }
</style>
</head>
<body>
<h1 class="novel-title">Tonari no Seki no Yatsu ga Souiu Me de Mitekuru</h1>
<div class="author">
<span>Author:</span>
<a href='#' title="updating" class="property-item">
<span itemprop="author">updating</span>
</a>
</div>
<div x-data="ratings_item" class="rating-star" itemprop="aggregateRating" itemscope="" itemtype="https://schema.org/AggregateRating" x-cloak>
  <strong>4.7<span style="font-size:12px"> (20)</span></strong>
  <meta itemprop="ratingValue" content="4.7">
  <meta itemprop="ratingCount" content="20">
  <meta itemprop="bestRating" content="5">
  <meta itemprop="worstRating" content="1">
</div>
<div class="header-stats">
<span>
<strong><i class="material-icons">book</i>    118-eng-li </strong>
<small>Chapters</small>
</span>
<span>
<strong><i class="material-icons">bookmark</i> 939</strong>
<small>Bookmarked</small>
</span>
<span>

<strong class="ongoing">Ongoing</strong>

<small>Status</small>
</span>
</div>
<div class="categories">
<strong>Categories</strong>
<ul>
<li>
<a href="/browse-comics/?genre_included=Comedy&minchaps=0" title="Comedy Genre" class="property-item">
Comedy
 </a>
</li>
<li>
<a href="/browse-comics/?genre_included= Ecchi&minchaps=0" title=" Ecchi Genre" class="property-item">
 Ecchi
 </a>
</li>
<li>
<a href="/browse-comics/?genre_included= School life&minchaps=0" title=" School life Genre" class="property-item">
 School life
 </a>
</li>
</ul>
</div>
<section id="info" class="tab-pane _on">
<p class="description">
Tonari no Seki no Yatsu ga Souiu Me de Mitekuru is a Manga/Manhwa/Manhua in english language, Comedy series, english chapters have been translated and you can read them here. You are reading  chapters on www.mgeko.cc fastest updating comic site. The Summary is

<br><br>
Of course, viewers are excited, but will those being watched feel the same excitement...?<br>
Brought to you by mmk of &#x27;Love Comedy Quest,&#x27; this is a pure love short comedy of the new era about &#x27;neighbors&#x27;!


<br>
<br>
</p>
<section x-data="{DisplayAll: false}">
  <h4 class="section-title" aria-level="2">Tags</h4>
  <div :class="DisplayAll ? 'manga-tags w-f' : 'manga-tags'" x-cloak>
    <div  class="mmtag-item" @click="window.location.href='/browse-comics/?tags_include=Role Reversal&minchaps=0'">
      <span class="selected-pin" >Role Reversal</span>
    </div>
    <div  class="mmtag-item" @click="window.location.href='/browse-comics/?tags_include=Classmate/s&minchaps=0'">
      <span class="selected-pin" >Classmate/s</span>
    </div>
  </div>
</section>
</section>
</body>
</html>`

const mgekoMetadataAllChaptersPage = `<!DOCTYPE html>
<html>
<body>
  <li data-chapterno="1">
    <a href="/reader/en/sample-series-chapter-118-eng-li/" title="Chapter 118">
      <strong class="chapter-title">118-eng-li</strong>
      <time class="chapter-update" datetime="Nov. 1, 2026, 6:00 p.m.">6 days</time>
    </a>
  </li>
</body>
</html>`

func newMgekoMetadataConnector(t *testing.T, mangaPage string) *Connector {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/sample-series/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mangaPage))
	})
	mux.HandleFunc("/manga/sample-series/all-chapters/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mgekoMetadataAllChaptersPage))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewConnectorWithOptions(server.URL, []string{"mgeko.cc"}, &http.Client{Timeout: 5 * time.Second})
}

// TestMgekoResolveFillsMetadataFromTheMangaPage pins where each field comes
// from: the categories as genres (trimmed), the tag pills as tags, the header
// stat as status, the rating microdata scaled from five stars, and the
// synopsis from after the generated blurb, never the templated meta copy. The
// "updating" author placeholder is dropped, and with no type label among the
// genres the type stays empty.
func TestMgekoResolveFillsMetadataFromTheMangaPage(t *testing.T) {
	conn := newMgekoMetadataConnector(t, mgekoMetadataMangaPage)

	resolved, err := conn.ResolveByURL(context.Background(), "https://www.mgeko.cc/manga/sample-series/")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata from the manga page")
	}
	if want := []string{"Comedy", "Ecchi", "School life"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if want := []string{"Role Reversal", "Classmate/s"}; !reflect.DeepEqual(meta.Tags, want) {
		t.Errorf("tags: got %v, want %v", meta.Tags, want)
	}
	if meta.Status != connectors.SeriesStatusOngoing {
		t.Errorf("status: got %q", meta.Status)
	}
	if len(meta.Authors) != 0 {
		t.Errorf("expected the updating placeholder to be dropped, got %v", meta.Authors)
	}
	if meta.Type != "" {
		t.Errorf("expected no type without a type label, got %q", meta.Type)
	}
	if meta.Rating == nil || *meta.Rating != 9.4 {
		t.Errorf("rating: got %v, want 9.4 (4.7 of 5)", meta.Rating)
	}
	if meta.RatingVotes != 20 {
		t.Errorf("rating votes: got %d", meta.RatingVotes)
	}
	wantDescription := "Of course, viewers are excited, but will those being watched feel the same excitement...?\nBrought to you by mmk of 'Love Comedy Quest,' this is a pure love short comedy of the new era about 'neighbors'!"
	if meta.Description != wantDescription {
		t.Errorf("description: got %q, want %q", meta.Description, wantDescription)
	}
}

// TestMgekoMetadataReadsTypeLabelAndRealAuthor pins the series whose genres
// carry a type label (solo-leveling-mg1 lists "manhwa" beside Action), a real
// author name and a completed status.
func TestMgekoMetadataReadsTypeLabelAndRealAuthor(t *testing.T) {
	page := `<html><body>
<h1 class="novel-title">Solo Leveling</h1>
<div class="author"><span>Author:</span><a href='#' title="Sung-Lak Jang" class="property-item"><span itemprop="author">Sung-Lak Jang</span></a></div>
<div class="header-stats"><span><strong class="completed">Completed</strong>
<small>Status</small></span></div>
<div class="categories"><strong>Categories</strong><ul>
<li><a href="/browse-comics/?genre_included=Action&minchaps=0" class="property-item">Action</a></li>
<li><a href="/browse-comics/?genre_included= Webtoons&minchaps=0" class="property-item"> Webtoons</a></li>
<li><a href="/browse-comics/?genre_included= manhwa&minchaps=0" class="property-item"> manhwa</a></li>
</ul></div>
</body></html>`
	conn := newMgekoMetadataConnector(t, page)

	resolved, err := conn.ResolveByURL(context.Background(), "https://www.mgeko.cc/manga/sample-series/")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	meta := resolved.Metadata
	if meta == nil {
		t.Fatal("expected metadata")
	}
	if meta.Type != connectors.SeriesTypeManhwa {
		t.Errorf("type: got %q, want manhwa from the genre label", meta.Type)
	}
	if meta.Status != connectors.SeriesStatusCompleted {
		t.Errorf("status: got %q", meta.Status)
	}
	if want := []string{"Sung-Lak Jang"}; !reflect.DeepEqual(meta.Authors, want) {
		t.Errorf("authors: got %v, want %v", meta.Authors, want)
	}
	if want := []string{"Action", "Webtoons", "manhwa"}; !reflect.DeepEqual(meta.Genres, want) {
		t.Errorf("genres: got %v, want %v", meta.Genres, want)
	}
	if meta.Description != "" || meta.Rating != nil {
		t.Errorf("expected no description or rating, got %q / %v", meta.Description, meta.Rating)
	}
}

// TestMgekoTypeFromGenresNeedsOneExactLabel pins that the type is only read
// from an exact manga/manhwa/manhua genre, and not when two disagree.
func TestMgekoTypeFromGenresNeedsOneExactLabel(t *testing.T) {
	cases := []struct {
		genres []string
		want   string
	}{
		{genres: []string{"Action", "Manhua"}, want: connectors.SeriesTypeManhua},
		{genres: []string{"Romance", "One shot"}, want: ""},
		{genres: []string{"Manga", "Manhwa"}, want: ""},
		{genres: nil, want: ""},
	}
	for _, tc := range cases {
		if got := typeFromGenres(tc.genres); got != tc.want {
			t.Errorf("typeFromGenres(%v) = %q, want %q", tc.genres, got, tc.want)
		}
	}
}

// TestMgekoMissingMetadataNeverFailsTheResolve pins that metadata is a bonus:
// a manga page with only the generated blurb, the author placeholder, an
// unrated score and a status outside the vocabulary leaves the metadata nil,
// as does the bare page, while the resolve still answers with its chapter.
func TestMgekoMissingMetadataNeverFailsTheResolve(t *testing.T) {
	blurbOnly := `<html><head>
<meta name="description" content="Sample Series is a manga/manhwa/manhua written by updating and is updated fast and free on www.mgeko.cc." />
</head><body>
<h1 class="novel-title">Sample Series</h1>
<span itemprop="author">Updating</span>
<meta itemprop="ratingValue" content="0.0">
<meta itemprop="ratingCount" content="0">
<strong class="unknown">Soon</strong> <small>Status</small>
<div class="categories"><strong>Categories</strong><ul></ul></div>
<p class="description">
Sample Series is a Manga/Manhwa/Manhua in english language, series, english chapters have been translated and you can read them here. You are reading chapters on www.mgeko.cc fastest updating comic site.
</p>
</body></html>`

	for name, page := range map[string]string{"blurb only": blurbOnly, "bare page": mgekoMangaPage} {
		t.Run(name, func(t *testing.T) {
			conn := newMgekoMetadataConnector(t, page)
			resolved, err := conn.ResolveByURL(context.Background(), "https://www.mgeko.cc/manga/sample-series/")
			if err != nil {
				t.Fatalf("missing metadata must not fail the resolve: %v", err)
			}
			if resolved.LatestChapter == nil || *resolved.LatestChapter != 118 {
				t.Fatalf("expected latest chapter 118, got %v", resolved.LatestChapter)
			}
			if resolved.Metadata != nil {
				t.Fatalf("expected no metadata, got %+v", resolved.Metadata)
			}
		})
	}
}
