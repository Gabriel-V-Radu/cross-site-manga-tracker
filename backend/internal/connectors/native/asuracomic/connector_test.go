package asuracomic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAsuraComicConnectorResolveAndSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("q")
		if name == "nano" {
			_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/comics/nano-machine-7f873ca6">ONGOING MANHWA Nano Machine Chapter 304 9.5</a>
  <a href="/comics/another-title-12345678">Another Title</a>
</body>
</html>`))
			return
		}

		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})

	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
	<meta property="og:image" content="https://cdn.asurascans.com/asura-images/covers/nano-social.webp">
</head>
<body>
	<img src="https://cdn.asurascans.com/asura-images/covers/nano-rendered-400.webp" alt="Nano Machine">
  <h1>Nano Machine</h1>
  <div>Alternative Names: Mechanical Cultivator | Nano Machine Reloaded</div>
  <div>Updated On</div><div>February 17th 2026</div>
  <a href="/comics/nano-machine-7f873ca6/chapter/303">Chapter 303</a>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	mux.HandleFunc("/comics/another-title-12345678", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <title>Another Title - Asura Scans</title>
</head>
<body>
  <a href="/comics/another-title-12345678/chapter/10">Chapter 10</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})

	if err := conn.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "nano-machine-7f873ca6" {
		t.Fatalf("expected source id nano-machine-7f873ca6, got %s", resolved.SourceItemID)
	}
	if resolved.Title != "Nano Machine" {
		t.Fatalf("expected title Nano Machine, got %s", resolved.Title)
	}
	if resolved.CoverImageURL != "https://cdn.asurascans.com/asura-images/covers/nano-rendered-400.webp" {
		t.Fatalf("unexpected cover image: %s", resolved.CoverImageURL)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 304 {
		t.Fatalf("expected latest chapter 304, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || resolved.LastUpdatedAt.Format("2006-01-02") != "2026-02-17" {
		t.Fatalf("expected updated date 2026-02-17, got %v", resolved.LastUpdatedAt)
	}

	results, err := conn.SearchByTitle(context.Background(), "nano", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].SourceItemID != "nano-machine-7f873ca6" {
		t.Fatalf("unexpected source id %s", results[0].SourceItemID)
	}
	if results[0].CoverImageURL != "https://cdn.asurascans.com/asura-images/covers/nano-rendered-400.webp" {
		t.Fatalf("unexpected search cover image: %s", results[0].CoverImageURL)
	}
	if results[0].LatestChapter == nil || *results[0].LatestChapter != 304 {
		t.Fatalf("expected latest chapter 304 from search, got %v", results[0].LatestChapter)
	}
}

func TestAsuraComicConnectorFallsBackToMetaCoverImage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
  <meta property="og:image" content="https://cdn.asurascans.com/asura-images/covers/nano-social.webp">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.CoverImageURL != "https://cdn.asurascans.com/asura-images/covers/nano-social.webp" {
		t.Fatalf("unexpected fallback cover image: %s", resolved.CoverImageURL)
	}
}

func TestAsuraComicConnectorRejectsNonAsuraURLs(t *testing.T) {
	conn := NewConnectorWithOptions("https://asurascans.com", []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	_, err := conn.ResolveByURL(context.Background(), "https://example.com/series/nano-machine-11b89554")
	if err == nil {
		t.Fatalf("expected error for non-asuracomic url")
	}
}

func TestAsuraComicConnectorSupportsBareChapterPathPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/chapter/298">Chapter 298 February 4th 2026</a>
  <a href="/chapter/299">Chapter 299 February 11th 2026</a>
  <div>Updated On</div><div>February 17th 2026</div>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.LatestChapter == nil || *resolved.LatestChapter != 299 {
		t.Fatalf("expected latest chapter 299, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil || resolved.LastUpdatedAt.Format("2006-01-02") != "2026-02-11" {
		t.Fatalf("expected latest chapter release date 2026-02-11, got %v", resolved.LastUpdatedAt)
	}
}

func TestAsuraComicConnectorUsesPublishedAtForLatestChapter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/298">Chapter 298 February 4th 2026</a>
  <a href="/comics/nano-machine-7f873ca6/chapter/299">Chapter 299 February 11th 2026</a>
  <script>self.__next_f.push([1,"{\"chapters\":[{\"name\":298,\"title\":\"A\",\"id\":1001,\"published_at\":\"2026-02-04T15:10:00.000000Z\"},{\"name\":299,\"title\":\"B\",\"id\":1002,\"published_at\":\"2026-02-11T16:44:55.000000Z\"}]}"])</script>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/nano-machine-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected published_at timestamp for latest chapter")
	}

	expected := time.Date(2026, time.February, 11, 16, 44, 55, 0, time.UTC)
	if !resolved.LastUpdatedAt.Equal(expected) {
		t.Fatalf("expected latest chapter release time %s, got %s", expected.Format(time.RFC3339), resolved.LastUpdatedAt.UTC().Format(time.RFC3339))
	}
}

func TestAsuraComicConnectorUsesPublishedAtFromHTMLSerializedChapters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
	})
	mux.HandleFunc("/comics/a-villains-will-to-survive-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="A Villain's Will to Survive - Asura Scans">
</head>
<body>
  <astro-island props="{&quot;chapters&quot;:[3,[[0,{&quot;number&quot;:[0,49],&quot;published_at&quot;:[0,&quot;2026-03-20T14:38:02Z&quot;]}],[0,{&quot;number&quot;:[0,48],&quot;published_at&quot;:[0,&quot;2026-03-14T14:06:02Z&quot;]}]]}"></astro-island>
  <a href="/comics/a-villains-will-to-survive-7f873ca6/chapter/49">Chapter 49</a>
  <div class="text-sm text-white/40">1 hour ago</div>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asurascans.com/comics/a-villains-will-to-survive-7f873ca6")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if resolved.LatestChapter == nil || *resolved.LatestChapter != 49 {
		t.Fatalf("expected latest chapter 49, got %v", resolved.LatestChapter)
	}
	if resolved.LastUpdatedAt == nil {
		t.Fatalf("expected published_at timestamp for latest chapter")
	}

	expected := time.Date(2026, time.March, 20, 14, 38, 2, 0, time.UTC)
	if !resolved.LastUpdatedAt.Equal(expected) {
		t.Fatalf("expected latest chapter release time %s, got %s", expected.Format(time.RFC3339), resolved.LastUpdatedAt.UTC().Format(time.RFC3339))
	}
}

func TestAsuraComicConnectorSearchSupportsSeriesHrefWithoutLeadingSlash(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "leveling solo" {
			if r.URL.Query().Get("q") != "leveling solo" {
				_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
				return
			}
		}

		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="comics/solo-leveling-ragnarok-c739e802">Solo Leveling: Ragnarok</a>
  <a href="comics/solo-leveling-26b0cf1b">Solo Leveling</a>
</body>
</html>`))
	})

	mux.HandleFunc("/comics/solo-leveling-ragnarok-c739e802", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Solo Leveling: Ragnarok - Asura Scans">
</head>
<body>
  <a href="/comics/solo-leveling-ragnarok-c739e802/chapter/68">Chapter 68</a>
</body>
</html>`))
	})

	mux.HandleFunc("/comics/solo-leveling-26b0cf1b", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Solo Leveling - Asura Scans">
</head>
<body>
  <a href="/comics/solo-leveling-26b0cf1b/chapter/200">Chapter 200</a>
</body>
</html>`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	results, err := conn.SearchByTitle(context.Background(), "leveling solo", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestAsuraComicConnectorFallsBackFromLegacySeriesURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "nano machine" {
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>ok</body></html>`))
			return
		}

		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<body>
  <a href="/comics/nano-machine-7f873ca6">Nano Machine</a>
</body>
</html>`))
	})
	mux.HandleFunc("/comics/nano-machine-7f873ca6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
  <meta property="og:title" content="Nano Machine - Asura Scans">
</head>
<body>
  <a href="/comics/nano-machine-7f873ca6/chapter/304">Chapter 304</a>
</body>
</html>`))
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/comics/nano-machine-11b89554" {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()

	conn := NewConnectorWithOptions(server.URL, []string{"asurascans.com", "asuracomic.net"}, &http.Client{Timeout: 5 * time.Second})
	resolved, err := conn.ResolveByURL(context.Background(), "https://asuracomic.net/series/nano-machine-11b89554")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved.SourceItemID != "nano-machine-7f873ca6" {
		t.Fatalf("expected canonical source id, got %s", resolved.SourceItemID)
	}

	chapterURL, err := conn.ResolveChapterURL(context.Background(), "https://asuracomic.net/series/nano-machine-11b89554", 304)
	if err != nil {
		t.Fatalf("resolve chapter url failed: %v", err)
	}
	if chapterURL != server.URL+"/comics/nano-machine-7f873ca6/chapter/304" {
		t.Fatalf("unexpected chapter url: %s", chapterURL)
	}
}
