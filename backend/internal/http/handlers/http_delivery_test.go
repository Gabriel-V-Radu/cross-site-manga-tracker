package handlers_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// How the server hands things over, as opposed to what it says: compressed or
// not, and how long a browser may keep it.

func deliver(t *testing.T, app *fiber.App, path string, acceptEncoding string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return res, body
}

// newFileServingEnv is a test app whose data directory is not under
// t.TempDir: the static handler keeps a served file open in its handle cache
// for a while after the request, Windows will not delete an open file, and
// TempDir fails the test when its removal does.
func newFileServingEnv(t *testing.T) testEnv {
	t.Helper()
	dataDir, err := os.MkdirTemp("", "delivery-test-")
	if err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	cfg := newTestConfig(t)
	cfg.DataDir = dataDir
	return newTestEnvWithConfig(t, nil, cfg)
}

func writeDataFile(t *testing.T, dir string, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// Pages, partials, scripts and the stylesheet are text and go out compressed
// to a client that takes it; they used to go out whole. Images in a
// compressed format are left as they are, whichever route serves them — it is
// the answer's type that decides, so an SVG logo, which is text, is
// compressed too — and so is everything for a client that did not ask.
func TestTextIsCompressedAndImagesAreNot(t *testing.T) {
	env := newFileServingEnv(t)
	defer env.cleanup()

	stylesheet, err := os.ReadFile(filepath.Join(env.cfg.AssetsDir(), "dashboard.css"))
	if err != nil {
		t.Fatalf("read the stylesheet: %v", err)
	}
	// Bytes that would compress well, under image names: they must be left
	// alone for the type they are served as, not for what is in the file.
	picture := bytes.Repeat([]byte("not really a picture "), 400)
	writeDataFile(t, env.cfg.CoversDir(), "cover.jpg", picture)
	writeDataFile(t, env.cfg.CoversDir(), "cover.webp", picture)
	writeDataFile(t, env.cfg.SourceLogosDir(), "logo.png", picture)
	writeDataFile(t, env.cfg.SourceLogosDir(), "logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg">`+strings.Repeat(`<rect width="10" height="10"/>`, 100)+`</svg>`))

	for _, path := range []string{"/assets/dashboard.css?v=1", "/dashboard", "/dashboard/trackers", "/uploads/site-logos/logo.svg"} {
		res, body := deliver(t, env.app, path, "gzip")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, res.StatusCode)
		}
		if got := res.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("GET %s: Content-Encoding %q, want gzip", path, got)
		}
		if !strings.Contains(res.Header.Get("Vary"), "Accept-Encoding") {
			t.Fatalf("GET %s: a compressed answer must say it varies by Accept-Encoding, got %q", path, res.Header.Get("Vary"))
		}
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("GET %s: the body is not gzip: %v", path, err)
		}
		plain, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("GET %s: decompress: %v", path, err)
		}
		if len(body) >= len(plain) {
			t.Fatalf("GET %s: %d bytes compressed from %d", path, len(body), len(plain))
		}
		if strings.HasPrefix(path, "/assets/") && !bytes.Equal(plain, stylesheet) {
			t.Fatalf("GET %s: the stylesheet did not survive the compression", path)
		}
	}

	if res, _ := deliver(t, env.app, "/assets/dashboard.css?v=1", "br"); res.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("a client that takes Brotli must get it, got %q", res.Header.Get("Content-Encoding"))
	}
	if res, body := deliver(t, env.app, "/assets/dashboard.css?v=1", ""); res.Header.Get("Content-Encoding") != "" || !bytes.Equal(body, stylesheet) {
		t.Fatalf("a client that asked for no compression got Content-Encoding %q", res.Header.Get("Content-Encoding"))
	}

	for _, path := range []string{"/covers/cover.jpg", "/covers/cover.webp", "/uploads/site-logos/logo.png"} {
		res, body := deliver(t, env.app, path, "gzip, br")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, res.StatusCode)
		}
		if got := res.Header.Get("Content-Encoding"); got != "" || !bytes.Equal(body, picture) {
			t.Fatalf("GET %s: an image went out re-encoded (Content-Encoding %q, %d bytes of %d)", path, got, len(body), len(picture))
		}
	}
}

// A URL that never serves different bytes is cached for good: an asset under
// the stamp the templates give it, an uploaded logo, a stored cover. Every
// page load used to ask about each script and the stylesheet again. Anything
// whose bytes can change under the same URL still revalidates, and a miss is
// never given the long lifetime.
func TestWhatNeverChangesIsCachedForGood(t *testing.T) {
	env := newFileServingEnv(t)
	defer env.cleanup()
	writeDataFile(t, env.cfg.SourceLogosDir(), "logo.png", []byte("\x89PNG\r\n\x1a\n"))

	const forGood = "public, max-age=31536000, immutable"
	cases := []struct {
		path       string
		wantStatus int
		wantCache  string
	}{
		{"/assets/dashboard.css?v=1763548800", http.StatusOK, forGood},
		{"/assets/dashboard-core.js?v=1763548800", http.StatusOK, forGood},
		{"/assets/favicon.svg?v=1763548800", http.StatusOK, forGood},
		{"/assets/dashboard.css", http.StatusOK, "no-cache"},
		{"/assets/dashboard.css?v=", http.StatusOK, "no-cache"},
		{"/assets/no-such-file.js?v=1763548800", http.StatusNotFound, "no-cache"},
		{"/uploads/site-logos/logo.png", http.StatusOK, forGood},
		{"/uploads/site-logos/no-such-logo.png", http.StatusNotFound, ""},
		{"/favicon.ico", http.StatusOK, "public, max-age=86400"},
	}
	for _, testCase := range cases {
		res, _ := deliver(t, env.app, testCase.path, "")
		if res.StatusCode != testCase.wantStatus {
			t.Errorf("GET %s: status %d, want %d", testCase.path, res.StatusCode, testCase.wantStatus)
			continue
		}
		if got := res.Header.Get("Cache-Control"); got != testCase.wantCache {
			t.Errorf("GET %s: Cache-Control %q, want %q", testCase.path, got, testCase.wantCache)
		}
	}
}

// The pages name their assets by stamped URL, the favicon included, or the
// long lifetime above would never apply to them.
func TestPagesNameTheirAssetsByStampedURL(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()

	assetReference := regexp.MustCompile(`(?:href|src)="(/assets/[^"]+)"`)
	for _, path := range []string{"/dashboard", "/dashboard/links"} {
		page := getMarkup(t, env.app, path)
		references := assetReference.FindAllStringSubmatch(page, -1)
		if len(references) == 0 {
			t.Fatalf("GET %s: no asset references found", path)
		}
		sawFavicon := false
		for _, reference := range references {
			if !regexp.MustCompile(`\?v=\d+$`).MatchString(reference[1]) {
				t.Errorf("GET %s: %s is not stamped, so it revalidates on every load", path, reference[1])
			}
			sawFavicon = sawFavicon || strings.HasPrefix(reference[1], "/assets/favicon.svg")
		}
		if !sawFavicon {
			t.Errorf("GET %s: the page names no favicon", path)
		}
	}
}

// The tags menu of the filters comes rendered with the page. Its "load"
// trigger asked for the same list again as the page opened, and swapped the
// ticks the server had just set from the URL for unticked copies the script
// then had to put back.
func TestTagsFilterMenuIsNotFetchedAgainOnLoad(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	if _, err := env.db.Exec(`INSERT INTO custom_tags (profile_id, name) VALUES (1, 'Favourites')`); err != nil {
		t.Fatalf("seed tag: %v", err)
	}

	page := getMarkup(t, env.app, "/dashboard?tags=Favourites")
	menu := regexp.MustCompile(`(?s)<div class="filter-multi-select__menu"\s+hx-get="/dashboard/profile/filter-tags"(.*?)>`).FindStringSubmatch(page)
	if menu == nil {
		t.Fatal("the tags menu is not in the page")
	}
	trigger := regexp.MustCompile(`hx-trigger="([^"]*)"`).FindStringSubmatch(menu[1])
	if trigger == nil || strings.Contains(trigger[1], "load") || !strings.Contains(trigger[1], "profileTagsChanged from:body") {
		t.Fatalf("the tags menu's trigger is %v, want only the tags changing", trigger)
	}
	if !regexp.MustCompile(`<input type="checkbox" name="tags" value="Favourites"[^>]*\bchecked\b`).MatchString(page) {
		t.Fatal("the page must come with the tag the URL names already ticked")
	}
}

// A card's own actions put their answer in a holder of their own. It used to
// go into #modal-zone, and an answer that came back after the reader had
// opened a modal emptied it. The holder has to be in the page for the buttons
// to have somewhere to send it, and it must not be hx-swap="none": the answer
// carries scripts, which htmx runs only for what it swaps.
func TestCardActionsAnswerOutsideTheModalZone(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	seedMarkupTracker(t, db, "Card Actions", 1, 10)

	page := getMarkup(t, app, "/dashboard")
	if !regexp.MustCompile(`<div id="card-action-sink" hidden></div>`).MatchString(page) {
		t.Fatal("the page has no #card-action-sink for the cards' buttons to answer into")
	}

	list := getMarkup(t, app, "/dashboard/trackers")
	button := regexp.MustCompile(`(?s)<button\b[^>]*hx-post="/dashboard/trackers/\d+/(set-last-read|delete)"[^>]*>`)
	found := button.FindAllStringSubmatch(list, -1)
	if len(found) != 2 {
		t.Fatalf("expected the card's Set last read and Delete buttons, found %d", len(found))
	}
	for _, match := range found {
		if !strings.Contains(match[0], `hx-target="#card-action-sink"`) || !strings.Contains(match[0], `hx-swap="innerHTML"`) {
			t.Errorf("the card's %s button does not answer into the holder: %s", match[1], markupSnippet(match[0], 300))
		}
	}
	// Edit opens a modal, and that is what #modal-zone is for.
	if !regexp.MustCompile(`(?s)<button\b[^>]*hx-get="/dashboard/trackers/\d+/edit"[^>]*hx-target="#modal-zone"`).MatchString(list) {
		t.Error("the card's Edit button must still open its modal in #modal-zone")
	}
}

// Every cover drawn from a URL a source supplied is asked for without a
// Referer: some image hosts refuse a request that names another site, and
// none of them needs to learn the address of this dashboard.
func TestCoverImagesAreRequestedWithoutAReferer(t *testing.T) {
	templates, err := filepath.Glob(filepath.Join(backendRootDir(), "web", "templates", "*.html"))
	if err != nil || len(templates) == 0 {
		t.Fatalf("list templates: %v (%d found)", err, len(templates))
	}
	coverImage := regexp.MustCompile(`<img\b[^>]*\{\{coverSrc [^>]*>`)
	covers := 0
	for _, path := range templates {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, tag := range coverImage.FindAllString(string(content), -1) {
			covers++
			if !strings.Contains(tag, `referrerpolicy="no-referrer"`) {
				t.Errorf("%s: a cover image is requested with a Referer: %s", filepath.Base(path), tag)
			}
		}
	}
	if covers < 3 {
		t.Fatalf("found %d cover images in the templates, want the card's, the search results' and the link candidates'", covers)
	}
}
