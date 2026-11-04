package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// relayCoverResolver answers FetchImage with a fixed image and records what it
// was asked for, so a test can tell a relayed request from a refused one.
type relayCoverResolver struct {
	fakeCoverResolver
	body      []byte
	ext       string
	ok        bool
	requested []string
}

func (r *relayCoverResolver) FetchImage(_ context.Context, coverURL string) ([]byte, string, bool) {
	r.requested = append(r.requested, coverURL)
	return r.body, r.ext, r.ok
}

func newRemoteCoverApp(covers coverResolver) *fiber.App {
	handler := &DashboardHandler{covers: covers}
	app := fiber.New()
	app.Get(remoteCoverPath, handler.RemoteCover)
	return app
}

// TestCoverSrcRelaysOnlyHostsThatChallengeBrowsers pins which covers the page
// routes through the server: MangaDex's, whose CDN sends the reader's browser to
// a bot challenge an <img> cannot pass. Everything else stays a direct link, so
// the relay costs nothing for the hosts that hotlink fine.
func TestCoverSrcRelaysOnlyHostsThatChallengeBrowsers(t *testing.T) {
	mangadex := "https://uploads.mangadex.org/covers/c7e5ac88/c3934c8d.jpg.256.jpg"
	cases := map[string]string{
		mangadex:                            remoteCoverPath + "?url=" + url.QueryEscape(mangadex),
		"  " + mangadex + " ":               remoteCoverPath + "?url=" + url.QueryEscape(mangadex),
		"https://thumb.mghcdn.com/a/b.jpg":  "https://thumb.mghcdn.com/a/b.jpg",
		"https://notmangadex.org/cover.jpg": "https://notmangadex.org/cover.jpg",
		"/covers/abc123.jpg":                "/covers/abc123.jpg",
		"":                                  "",
	}
	for raw, want := range cases {
		if got := coverSrc(raw); got != want {
			t.Errorf("coverSrc(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestRemoteCoverRelaysAChallengedHost serves the image the resolver fetched,
// typed by its extension and cacheable by the browser.
func TestRemoteCoverRelaysAChallengedHost(t *testing.T) {
	covers := &relayCoverResolver{body: []byte("\x89PNG\r\n\x1a\n"), ext: ".png", ok: true}
	app := newRemoteCoverApp(covers)

	coverURL := "https://uploads.mangadex.org/covers/a/b.png.256.jpg"
	res, err := app.Test(httptest.NewRequest(http.MethodGet, coverSrc(coverURL), nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("expected image/png, got %q", got)
	}
	if res.Header.Get("Cache-Control") == "" {
		t.Fatalf("expected the relayed image to be cacheable")
	}
	if string(body) != string(covers.body) {
		t.Fatalf("expected the fetched bytes relayed, got %q", body)
	}
	if len(covers.requested) != 1 || covers.requested[0] != coverURL {
		t.Fatalf("expected one fetch of %q, got %v", coverURL, covers.requested)
	}
}

// TestRemoteCoverRefusesAnythingElse keeps the relay from being a general
// fetcher: only https URLs on a host that challenges browsers reach the network.
func TestRemoteCoverRefusesAnythingElse(t *testing.T) {
	covers := &relayCoverResolver{body: []byte("x"), ext: ".png", ok: true}
	app := newRemoteCoverApp(covers)

	for _, target := range []string{
		"",
		"https://example.com/cover.jpg",
		"https://notmangadex.org/cover.jpg",
		"http://uploads.mangadex.org/covers/a/b.jpg",
		"https://127.0.0.1/cover.jpg",
		"file:///etc/passwd",
	} {
		res, err := app.Test(httptest.NewRequest(http.MethodGet, remoteCoverPath+"?url="+url.QueryEscape(target), nil))
		if err != nil {
			t.Fatalf("request %q: %v", target, err)
		}
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: expected 400, got %d", target, res.StatusCode)
		}
	}
	if len(covers.requested) != 0 {
		t.Fatalf("a refused URL must not be fetched, got %v", covers.requested)
	}
}

// TestRemoteCoverReportsAFailedFetch answers 502 when the image host refused or
// returned something that is not an image, so the <img> shows as broken rather
// than rendering an error body.
func TestRemoteCoverReportsAFailedFetch(t *testing.T) {
	app := newRemoteCoverApp(&relayCoverResolver{ok: false})

	res, err := app.Test(httptest.NewRequest(http.MethodGet, coverSrc("https://uploads.mangadex.org/covers/a/b.jpg"), nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", res.StatusCode)
	}
}
