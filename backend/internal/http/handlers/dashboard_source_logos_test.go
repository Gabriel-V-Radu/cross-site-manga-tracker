package handlers_test

import (
	"bytes"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

func TestSaveSourceLogosFromMenuUploadsPNG(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

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
		FROM profile_source_logos
		WHERE profile_id = ? AND source_id = ?
	`, 1, sourceID).Scan(&logoPath); err != nil {
		t.Fatalf("read saved source logo row: %v", err)
	}
	if !strings.HasPrefix(logoPath, "/uploads/site-logos/") {
		t.Fatalf("expected saved logo path in uploads directory, got %q", logoPath)
	}

	diskPath := filepath.Join("data", "uploads", "site-logos", filepath.Base(strings.TrimPrefix(logoPath, "/uploads/site-logos/")))
	if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("cleanup uploaded logo file: %v", err)
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
		INSERT INTO profile_source_logos (profile_id, source_id, logo_url)
		VALUES (?, ?, ?)
	`, 1, sourceID, "/uploads/site-logos/custom-logo.png")
	if err != nil {
		t.Fatalf("seed profile source logo: %v", err)
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

func sourceMetaByKey(t *testing.T, db *sql.DB, key string) (int64, string) {
	t.Helper()

	var sourceID int64
	var sourceName string
	if err := db.QueryRow(`SELECT id, name FROM sources WHERE key = ?`, key).Scan(&sourceID, &sourceName); err != nil {
		t.Fatalf("lookup source %s: %v", key, err)
	}

	return sourceID, sourceName
}
