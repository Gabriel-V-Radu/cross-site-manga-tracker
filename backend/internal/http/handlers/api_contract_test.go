package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// The URL decides which site a tracker belongs to on the API as on the form:
// sourceId 4 (AsuraComic) sent with a MangaDex link used to be stored as an
// Asura tracker, a pair no poll can read.
func TestAPIStoresTheSourceItsURLBelongsTo(t *testing.T) {
	db, app, cleanup := setupTestAppWithRegistry(t, func(registry *connectors.Registry) {
		for key, host := range map[string]string{"mangadex": "mangadex.org", "asuracomic": "asurascans.com"} {
			if err := registry.Register(unreachableConnector{key: key, hosts: []string{host}}); err != nil {
				t.Fatalf("register %s: %v", key, err)
			}
		}
	})
	defer cleanup()

	mangadexID, _ := sourceMetaByKey(t, db, "mangadex")
	asuraID, _ := sourceMetaByKey(t, db, "asuracomic")

	readSourceID := func(id int64) int64 {
		t.Helper()
		var sourceID int64
		if err := db.QueryRow(`SELECT source_id FROM trackers WHERE id = ?`, id).Scan(&sourceID); err != nil {
			t.Fatalf("read source id: %v", err)
		}
		return sourceID
	}

	body := map[string]any{
		"title":     "Mislabelled",
		"sourceId":  asuraID,
		"sourceUrl": "https://mangadex.org/title/mislabelled",
		"status":    "reading",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/trackers", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(res.Body)
		t.Fatalf("create: expected 201, got %d (body: %s)", res.StatusCode, payload)
	}
	var created struct {
		ID       int64 `json:"id"`
		SourceID int64 `json:"sourceId"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.SourceID != mangadexID || readSourceID(created.ID) != mangadexID {
		t.Fatalf("create stored source %d (answered %d), want MangaDex (%d)", readSourceID(created.ID), created.SourceID, mangadexID)
	}

	// An update that names the wrong site again is corrected the same way, and
	// one whose URL moved to another site follows the URL.
	path := "/v1/trackers/" + strconv.FormatInt(created.ID, 10)
	if status, message := sendJSON(t, app, http.MethodPut, path, body); status != http.StatusOK {
		t.Fatalf("update: expected 200, got %d %q", status, message)
	}
	if got := readSourceID(created.ID); got != mangadexID {
		t.Fatalf("update stored source %d, want MangaDex (%d)", got, mangadexID)
	}
	body["sourceId"] = mangadexID
	body["sourceUrl"] = "https://asurascans.com/series/mislabelled"
	if status, message := sendJSON(t, app, http.MethodPut, path, body); status != http.StatusOK {
		t.Fatalf("update to asura: expected 200, got %d %q", status, message)
	}
	if got := readSourceID(created.ID); got != asuraID {
		t.Fatalf("update stored source %d, want AsuraComic (%d)", got, asuraID)
	}

	// A URL no connector claims leaves the sourceId as sent.
	body["sourceUrl"] = "https://unclaimed.example/series/mislabelled"
	if status, message := sendJSON(t, app, http.MethodPut, path, body); status != http.StatusOK {
		t.Fatalf("update to unclaimed host: expected 200, got %d %q", status, message)
	}
	if got := readSourceID(created.ID); got != mangadexID {
		t.Fatalf("unclaimed URL stored source %d, want the sent MangaDex (%d)", got, mangadexID)
	}
}

// A write whose body is not declared as JSON is refused with 415. A JSON body
// sent without the header — or with the form type curl -d and Python's urllib
// add by default — used to be decoded as an empty form and refused with "title
// is required", about a body that had a title.
func TestAPIWritesRequireAJSONBody(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedGuardTracker(t, db, 1, "Typed Body")

	const body = `{"title":"Typed Body","sourceId":1,"sourceUrl":"https://mangadex.org/title/typed-body","status":"reading"}`
	for _, target := range []struct{ method, path string }{
		{http.MethodPost, "/v1/trackers"},
		{http.MethodPut, "/v1/trackers/" + strconv.FormatInt(id, 10)},
	} {
		for _, contentType := range []string{"", "application/x-www-form-urlencoded", "text/plain"} {
			req := httptest.NewRequest(target.method, target.path, strings.NewReader(body))
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			res, err := app.Test(req, 10000)
			if err != nil {
				t.Fatalf("%s %s: %v", target.method, target.path, err)
			}
			payload, _ := io.ReadAll(res.Body)
			var decoded struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(payload, &decoded)
			if res.StatusCode != http.StatusUnsupportedMediaType || !strings.Contains(decoded.Message, "application/json") {
				t.Fatalf("%s %s with Content-Type %q: expected 415 naming application/json, got %d %s", target.method, target.path, contentType, res.StatusCode, payload)
			}
		}
	}
	if got := countTrackers(t, db); got != 1 {
		t.Fatalf("refused writes must not create anything, found %d trackers", got)
	}

	// The type may carry parameters.
	req := httptest.NewRequest(http.MethodPost, "/v1/trackers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("POST with charset: %v", err)
	}
	if res.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(res.Body)
		t.Fatalf("POST with charset: expected 201, got %d (body: %s)", res.StatusCode, payload)
	}
}

// An unknown endpoint or a method the endpoint does not take answers in the
// API's JSON shape. Both used to answer fiber's plain text ("Cannot GET …"),
// which a client decoding every answer as {"message": …} could not read.
func TestAPIUnknownRoutesAnswerJSON(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	cases := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/v1/nope", http.StatusNotFound},
		{http.MethodPost, "/v1/trackers/1/nope", http.StatusNotFound},
		{http.MethodPatch, "/v1/trackers/1", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1", http.StatusNotFound},
	}
	for _, tc := range cases {
		res, err := app.Test(httptest.NewRequest(tc.method, tc.path, nil), 10000)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		payload, _ := io.ReadAll(res.Body)
		var decoded struct {
			Message string `json:"message"`
		}
		if res.StatusCode != tc.status || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") ||
			json.Unmarshal(payload, &decoded) != nil || decoded.Message == "" {
			t.Fatalf("%s %s: expected %d with a JSON message, got %d %q %s", tc.method, tc.path, tc.status, res.StatusCode, res.Header.Get("Content-Type"), payload)
		}
	}

	// The dashboard keeps fiber's plain text, which the page shows as is.
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/dashboard/nope", nil), 10000)
	if err != nil {
		t.Fatalf("GET /dashboard/nope: %v", err)
	}
	if res.StatusCode != http.StatusNotFound || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("dashboard 404: expected plain text, got %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// Only a cover actually served is cacheable forever. The header used to be set
// ahead of the static handler, so a 404 for a cover not on disk yet carried
// "immutable" too and a browser could keep that miss for a year.
func TestCoversOnlyCacheFilesThatExist(t *testing.T) {
	// Not under t.TempDir: the static handler keeps a served file open in its
	// handle cache for a while after the request, Windows will not delete an
	// open file, and TempDir fails the test when its removal does.
	dataDir, err := os.MkdirTemp("", "covers-cache-test-")
	if err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	cfg := newTestConfig(t)
	cfg.DataDir = dataDir
	env := newTestEnvWithConfig(t, nil, cfg)
	defer env.cleanup()

	if err := os.MkdirAll(env.cfg.CoversDir(), 0o755); err != nil {
		t.Fatalf("create covers dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.cfg.CoversDir(), "present.jpg"), tinyJPEG, 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}

	res, err := env.app.Test(httptest.NewRequest(http.MethodGet, "/covers/present.jpg", nil), 10000)
	if err != nil {
		t.Fatalf("GET present cover: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("present cover: expected 200 immutable, got %d %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}

	res, err = env.app.Test(httptest.NewRequest(http.MethodGet, "/covers/missing.jpg", nil), 10000)
	if err != nil {
		t.Fatalf("GET missing cover: %v", err)
	}
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing cover: expected 404, got %d", res.StatusCode)
	}
	if cacheControl := res.Header.Get("Cache-Control"); strings.Contains(cacheControl, "immutable") || strings.Contains(cacheControl, "max-age") {
		t.Fatalf("a missing cover must not be cacheable, got Cache-Control %q", cacheControl)
	}
}
