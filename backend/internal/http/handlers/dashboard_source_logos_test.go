package handlers_test

import (
	"bytes"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

var tinyJPEG = []byte{
	0xff, 0xd8, 0xff, 0xd9,
}

func TestSaveSourceLogosFromMenuUploadsPNG(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	db, app := env.db, env.app

	sourceID, _ := sourceMetaByKey(t, db, "mangadex")

	_, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, 1, "Logo Upload Seed", sourceID, "https://mangadex.org/title/logo-upload-seed", "reading", 1.0, 2.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	filePart, err := writer.CreateFormFile("source_logo_file_"+toString(int(sourceID)), "mangadex-logo.png")
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	if _, err := filePart.Write(tinyPNG); err != nil {
		t.Fatalf("write multipart file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/source-logos?profile=profile1", &payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("save source logos request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read save source logos response: %v", err)
	}
	if !strings.Contains(string(body), "Linked site logos saved") {
		t.Fatalf("expected source logo success message")
	}

	var logoPath string
	if err := db.QueryRow(`
		SELECT logo_url
		FROM source_logos
		WHERE source_id = ?
	`, sourceID).Scan(&logoPath); err != nil {
		t.Fatalf("read saved source logo row: %v", err)
	}
	if !strings.HasPrefix(logoPath, "/uploads/site-logos/") {
		t.Fatalf("expected saved logo path in uploads directory, got %q", logoPath)
	}
	// The file is one per site now, so the name must not carry the profile that
	// happened to upload it.
	if strings.Contains(logoPath, "profile-") {
		t.Fatalf("logo file name must not be namespaced by profile, got %q", logoPath)
	}

	// The stored path is what the browser asks /uploads for, so the file has to
	// exist under the directory that route serves.
	diskPath := filepath.Join(env.cfg.SourceLogosDir(), filepath.Base(strings.TrimPrefix(logoPath, "/uploads/site-logos/")))
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("uploaded logo missing from the configured uploads directory: %v", err)
	}
}

func TestSaveSourceLogosFromMenuUploadsJPG(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	db, app := env.db, env.app

	sourceID, _ := sourceMetaByKey(t, db, "mangadex")

	_, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, 1, "Logo Upload JPG Seed", sourceID, "https://mangadex.org/title/logo-upload-jpg-seed", "reading", 1.0, 2.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	filePart, err := writer.CreateFormFile("source_logo_file_"+toString(int(sourceID)), "mangadex-logo.jpg")
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	if _, err := filePart.Write(tinyJPEG); err != nil {
		t.Fatalf("write multipart file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/source-logos?profile=profile1", &payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("save source logos request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read save source logos response: %v", err)
	}
	if !strings.Contains(string(body), "Linked site logos saved") {
		t.Fatalf("expected source logo success message")
	}

	var logoPath string
	if err := db.QueryRow(`
		SELECT logo_url
		FROM source_logos
		WHERE source_id = ?
	`, sourceID).Scan(&logoPath); err != nil {
		t.Fatalf("read saved source logo row: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(logoPath), ".jpg") {
		t.Fatalf("expected saved logo path to end in .jpg, got %q", logoPath)
	}

	diskPath := filepath.Join(env.cfg.SourceLogosDir(), filepath.Base(strings.TrimPrefix(logoPath, "/uploads/site-logos/")))
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("uploaded logo missing from the configured uploads directory: %v", err)
	}
}

func TestSaveSourceLogosFromMenuOversizedUploadShowsValidationMessage(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	sourceID, _ := sourceMetaByKey(t, db, "mangadex")

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	filePart, err := writer.CreateFormFile("source_logo_file_"+toString(int(sourceID)), "mangadex-logo.png")
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	largePNG := make([]byte, 3<<20)
	if _, err := filePart.Write(largePNG); err != nil {
		t.Fatalf("write multipart file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/source-logos?profile=profile1", &payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("save source logos request failed: %v", err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read save source logos response: %v", err)
	}
	// A refusal is an error status with the reason as the whole body, which
	// the page shows beside the form — not the menu re-rendered with the
	// reason in its success banner.
	if res.StatusCode != http.StatusBadRequest || strings.TrimSpace(string(body)) != "MangaDex: file too large (max 2MB)" {
		t.Fatalf("expected 400 \"MangaDex: file too large (max 2MB)\", got %d (body: %s)", res.StatusCode, string(body))
	}
}

// postSourceLogoFile uploads one file for one site through the profile menu,
// the way the menu's upload form does.
func postSourceLogoFile(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, sourceID int64, fileName string, content []byte) (int, string) {
	t.Helper()
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	filePart, err := writer.CreateFormFile("source_logo_file_"+toString(int(sourceID)), fileName)
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	if _, err := filePart.Write(content); err != nil {
		t.Fatalf("write multipart file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/source-logos?profile=profile1", &payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("save source logos request failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(body))
}

// A file that is not what its name says is refused with a 400 naming the site,
// and the logo already in place survives: it used to come back as a 200 with
// "ComicK: invalid PNG file" in the menu's green success banner.
func TestSaveSourceLogosFromMenuRefusesInvalidImage(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	db, app := env.db, env.app

	sourceID, sourceName := sourceMetaByKey(t, db, "comick")

	if status, body := postSourceLogoFile(t, app, sourceID, "comick.png", tinyPNG); status != http.StatusOK {
		t.Fatalf("seed upload: expected 200, got %d (body: %s)", status, body)
	}
	var storedPath string
	if err := db.QueryRow(`SELECT logo_url FROM source_logos WHERE source_id = ?`, sourceID).Scan(&storedPath); err != nil {
		t.Fatalf("read seeded logo row: %v", err)
	}

	cases := []struct {
		fileName string
		content  []byte
		want     string
	}{
		{fileName: "comick.png", content: []byte("this is not an image"), want: sourceName + ": invalid PNG file"},
		{fileName: "comick.svg", content: []byte(`<svg onload="alert(1)"></svg>`), want: sourceName + ": unsafe SVG content"},
		{fileName: "comick.gif", content: []byte("GIF89a"), want: sourceName + ": only .png, .svg, .jpg, .jpeg, or .webp files are allowed"},
	}
	for _, tc := range cases {
		status, body := postSourceLogoFile(t, app, sourceID, tc.fileName, tc.content)
		if status != http.StatusBadRequest || body != tc.want {
			t.Fatalf("%s: expected 400 %q, got %d %q", tc.fileName, tc.want, status, body)
		}
	}

	var afterPath string
	if err := db.QueryRow(`SELECT logo_url FROM source_logos WHERE source_id = ?`, sourceID).Scan(&afterPath); err != nil {
		t.Fatalf("read logo row after refusals: %v", err)
	}
	if afterPath != storedPath {
		t.Fatalf("a refused upload must leave the stored logo alone: %q became %q", storedPath, afterPath)
	}
	diskPath := filepath.Join(env.cfg.SourceLogosDir(), filepath.Base(storedPath))
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("the stored logo's file must survive a refused upload: %v", err)
	}
	entries, err := os.ReadDir(env.cfg.SourceLogosDir())
	if err != nil {
		t.Fatalf("list logo directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("refused uploads must leave no file behind, found %d files", len(entries))
	}
}

// The files of a save are only touched once its rows are written. The old logo
// used to be deleted before the write, so a save that then failed left the row
// naming a file that was gone, and every card showed a broken image.
func TestSaveSourceLogosFromMenuFailedSaveKeepsTheStoredFile(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	db, app := env.db, env.app

	sourceID, _ := sourceMetaByKey(t, db, "mangadex")
	if status, body := postSourceLogoFile(t, app, sourceID, "mangadex.png", tinyPNG); status != http.StatusOK {
		t.Fatalf("seed upload: expected 200, got %d (body: %s)", status, body)
	}
	var storedPath string
	if err := db.QueryRow(`SELECT logo_url FROM source_logos WHERE source_id = ?`, sourceID).Scan(&storedPath); err != nil {
		t.Fatalf("read seeded logo row: %v", err)
	}

	// The row can still be read, but no longer written.
	for _, event := range []string{"UPDATE", "DELETE", "INSERT"} {
		if _, err := db.Exec(`CREATE TRIGGER block_logo_` + strings.ToLower(event) + ` BEFORE ` + event + ` ON source_logos
			BEGIN SELECT RAISE(ABORT, 'logo writes blocked'); END`); err != nil {
			t.Fatalf("block %s: %v", event, err)
		}
	}

	if status, body := postSourceLogoFile(t, app, sourceID, "mangadex-new.png", tinyPNG); status != http.StatusInternalServerError || body != "Failed to save linked site logos" {
		t.Fatalf("replace: expected 500 \"Failed to save linked site logos\", got %d %q", status, body)
	}
	removeRes := postForm(t, app, "/dashboard/profile/source-logos?profile=profile1", url.Values{
		"source_logo_clear_" + toString(int(sourceID)): {"1"},
	}, nil)
	if removeRes.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(removeRes.Body)
		t.Fatalf("remove: expected 500, got %d (body: %s)", removeRes.StatusCode, body)
	}

	if _, err := os.Stat(filepath.Join(env.cfg.SourceLogosDir(), filepath.Base(storedPath))); err != nil {
		t.Fatalf("the file the row still names must survive the failed saves: %v", err)
	}
	entries, err := os.ReadDir(env.cfg.SourceLogosDir())
	if err != nil {
		t.Fatalf("list logo directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the failed replace must remove the file it wrote, found %d files", len(entries))
	}
}

// Asking to upload and remove the same site's logo at once is the caller's
// mistake, answered as such.
func TestSaveSourceLogosFromMenuRefusesUploadAndRemoveTogether(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	db, app := env.db, env.app

	sourceID, sourceName := sourceMetaByKey(t, db, "mangadex")

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	filePart, err := writer.CreateFormFile("source_logo_file_"+toString(int(sourceID)), "mangadex.png")
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	if _, err := filePart.Write(tinyPNG); err != nil {
		t.Fatalf("write multipart file part: %v", err)
	}
	if err := writer.WriteField("source_logo_clear_"+toString(int(sourceID)), "1"); err != nil {
		t.Fatalf("write clear field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/source-logos?profile=profile1", &payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("save source logos request failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	want := sourceName + ": choose either upload or remove"
	if res.StatusCode != http.StatusBadRequest || strings.TrimSpace(string(body)) != want {
		t.Fatalf("expected 400 %q, got %d %q", want, res.StatusCode, body)
	}
}

func TestTrackerCardShowsSourceNameWhenNoCustomLogo(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	sourceID, sourceName := sourceMetaByKey(t, db, "mangadex")

	_, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`, 1, "Fallback Logo Card", sourceID, "https://mangadex.org/title/fallback-logo-card", "reading", 12.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?profile=profile1&view=grid", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("trackers partial request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read trackers partial response: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "tracker-card__source-logo-text") {
		t.Fatalf("expected tracker card source text fallback")
	}
	if !strings.Contains(html, sourceName) {
		t.Fatalf("expected tracker card source name fallback to include source name")
	}
	if strings.Contains(html, "/assets/tracking-sites-logos/mangadex-logo.png") {
		t.Fatalf("expected tracker card to avoid default static source logo asset")
	}
}

func TestTrackerCardUsesCustomSourceLogoPath(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	sourceID, sourceName := sourceMetaByKey(t, db, "mangadex")

	_, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`, 1, "Custom Logo Card", sourceID, "https://mangadex.org/title/custom-logo-card", "reading", 7.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO source_logos (source_id, logo_url)
		VALUES (?, ?)
	`, sourceID, "/uploads/site-logos/custom-logo.png")
	if err != nil {
		t.Fatalf("seed source logo: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/trackers?profile=profile1&view=grid", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("trackers partial request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read trackers partial response: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "src=\"/uploads/site-logos/custom-logo.png\"") {
		t.Fatalf("expected tracker card to render custom source logo path")
	}
	if strings.Contains(html, "<span class=\"tracker-card__source-logo-text\">"+sourceName+"</span>") {
		t.Fatalf("expected image logo to replace source-name fallback")
	}
}

// A logo uploaded from one profile's menu is the other profile's logo too;
// that is the whole point of storing one per site.
func TestSourceLogoUploadedFromOneProfileShowsOnTheOther(t *testing.T) {
	env := newTestEnv(t, nil)
	defer env.cleanup()
	db, app := env.db, env.app

	sourceID, sourceName := sourceMetaByKey(t, db, "mangadex")

	// The tracker lives in profile 2; the upload happens from profile 1.
	_, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?)
	`, 2, "Other Profile Card", sourceID, "https://mangadex.org/title/other-profile-card", "reading", 3.0)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	filePart, err := writer.CreateFormFile("source_logo_file_"+toString(int(sourceID)), "mangadex-logo.png")
	if err != nil {
		t.Fatalf("create multipart file part: %v", err)
	}
	if _, err := filePart.Write(tinyPNG); err != nil {
		t.Fatalf("write multipart file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/dashboard/profile/source-logos?profile=profile1", &payload)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	uploadRes, err := app.Test(upload)
	if err != nil {
		t.Fatalf("save source logos request failed: %v", err)
	}
	if uploadRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(uploadRes.Body)
		t.Fatalf("expected 200, got %d (body: %s)", uploadRes.StatusCode, string(body))
	}

	var logoPath string
	if err := db.QueryRow(`SELECT logo_url FROM source_logos WHERE source_id = ?`, sourceID).Scan(&logoPath); err != nil {
		t.Fatalf("read saved source logo row: %v", err)
	}

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/dashboard/trackers?profile=profile2&view=grid", nil))
	if err != nil {
		t.Fatalf("trackers partial request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, string(body))
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read trackers partial response: %v", err)
	}
	html := string(body)

	if !strings.Contains(html, "src=\""+logoPath+"\"") {
		t.Fatalf("profile 2's card must show the logo uploaded from profile 1 (%s); body: %s", logoPath, html)
	}
	if strings.Contains(html, "<span class=\"tracker-card__source-logo-text\">"+sourceName+"</span>") {
		t.Fatalf("expected image logo to replace source-name fallback on profile 2")
	}
}

func sourceMetaByKey(t *testing.T, db *sql.DB, key string) (int64, string) {
	t.Helper()

	var sourceID int64
	var sourceName string
	if err := db.QueryRow(`SELECT id, name FROM sources WHERE key = ?`, key).Scan(&sourceID, &sourceName); err != nil {
		t.Fatalf("lookup source %s: %v", key, err)
	}

	return sourceID, sourceName
}
