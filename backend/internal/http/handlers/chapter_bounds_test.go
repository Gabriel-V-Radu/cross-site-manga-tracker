package handlers_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// sendJSON sends body as a JSON request and decodes the {"message": …} an
// error answer carries; message is empty for a success.
func sendJSON(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, method, path string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	payload, _ := io.ReadAll(res.Body)
	if res.StatusCode < http.StatusBadRequest {
		return res.StatusCode, ""
	}
	var decoded struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("%s %s: error answer is not JSON: %d %s", method, path, res.StatusCode, payload)
	}
	return res.StatusCode, decoded.Message
}

func storedChapters(t *testing.T, db *sql.DB, id int64) (sql.NullFloat64, sql.NullFloat64) {
	t.Helper()
	var lastRead, latest sql.NullFloat64
	if err := db.QueryRow(`SELECT last_read_chapter, latest_known_chapter FROM trackers WHERE id = ?`, id).Scan(&lastRead, &latest); err != nil {
		t.Fatalf("read chapters: %v", err)
	}
	return lastRead, latest
}

// A chapter number outside 0..100000 is refused on create. -5 used to be
// stored and shown as "Ch. -5", and 1e300 as a 301-digit chapter that made the
// page 2940px wide on a phone.
func TestAPICreateRejectsOutOfRangeChapters(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	refused := []struct {
		field string
		value float64
		want  string
	}{
		{field: "lastReadChapter", value: -5, want: "lastReadChapter must be between 0 and 100000"},
		{field: "latestKnownChapter", value: 1e300, want: "latestKnownChapter must be between 0 and 100000"},
		{field: "latestKnownChapter", value: 100000.5, want: "latestKnownChapter must be between 0 and 100000"},
		{field: "lastReadChapter", value: -0.5, want: "lastReadChapter must be between 0 and 100000"},
	}
	for _, tc := range refused {
		body := map[string]any{
			"title":     "Out Of Range",
			"sourceId":  1,
			"sourceUrl": "https://mangadex.org/title/out-of-range",
			"status":    "reading",
			tc.field:    tc.value,
		}
		status, message := sendJSON(t, app, http.MethodPost, "/v1/trackers", body)
		if status != http.StatusBadRequest || message != tc.want {
			t.Fatalf("%s=%v: expected 400 %q, got %d %q", tc.field, tc.value, tc.want, status, message)
		}
	}
	if got := countTrackers(t, db); got != 0 {
		t.Fatalf("refused creates must write nothing, found %d trackers", got)
	}

	// The edges and a real decimal chapter are fine.
	for _, chapters := range [][2]float64{{0, 0}, {1789, 1829.3}, {100000, 100000}} {
		body := map[string]any{
			"title":              "In Range",
			"sourceId":           1,
			"sourceUrl":          "https://mangadex.org/title/in-range",
			"status":             "reading",
			"lastReadChapter":    chapters[0],
			"latestKnownChapter": chapters[1],
		}
		if status, message := sendJSON(t, app, http.MethodPost, "/v1/trackers", body); status != http.StatusCreated {
			t.Fatalf("chapters %v: expected 201, got %d %q", chapters, status, message)
		}
	}
}

// A PUT is held to the same bounds for a number it changes, but a number sent
// back as stored passes: the poller stores whatever a site reports, and a
// client echoing the body GET returned must not be refused over it.
func TestAPIUpdateChecksOnlyChangedChapters(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (1, 'Odd Numbering', 1, 'https://mangadex.org/title/odd-numbering', 'reading', 12, 250000)
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, _ := result.LastInsertId()
	path := "/v1/trackers/" + strconv.FormatInt(id, 10)

	getRes, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("get tracker: %v", err)
	}
	var echoed map[string]any
	if err := json.NewDecoder(getRes.Body).Decode(&echoed); err != nil {
		t.Fatalf("decode tracker: %v", err)
	}
	echoed["status"] = "on_hold"
	if status, message := sendJSON(t, app, http.MethodPut, path, echoed); status != http.StatusOK {
		t.Fatalf("echoed PUT: expected 200, got %d %q", status, message)
	}

	for field, value := range map[string]float64{"lastReadChapter": -1, "latestKnownChapter": 1e300} {
		changed := map[string]any{}
		for key, v := range echoed {
			changed[key] = v
		}
		changed[field] = value
		status, message := sendJSON(t, app, http.MethodPut, path, changed)
		want := field + " must be between 0 and 100000"
		if status != http.StatusBadRequest || message != want {
			t.Fatalf("%s=%v: expected 400 %q, got %d %q", field, value, want, status, message)
		}
	}

	lastRead, latest := storedChapters(t, db, id)
	if lastRead.Float64 != 12 || latest.Float64 != 250000 {
		t.Fatalf("refused updates must write nothing, chapters are now %v / %v", lastRead, latest)
	}
}

// The dashboard form takes the same bounds. strconv.ParseFloat accepts "NaN"
// and "Inf" as readily as "-5", and each used to be stored.
func TestDashboardCreateRejectsOutOfRangeChapters(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	refused := []struct {
		field string
		value string
		want  string
	}{
		{field: "last_read_chapter", value: "-5", want: "Last read chapter must be between 0 and 100000"},
		{field: "latest_known_chapter", value: "1e300", want: "Latest known chapter must be between 0 and 100000"},
		{field: "latest_known_chapter", value: "NaN", want: "Latest known chapter must be a number"},
		{field: "last_read_chapter", value: "Inf", want: "Last read chapter must be a number"},
	}
	for _, tc := range refused {
		form := url.Values{}
		form.Set("title", "Out Of Range")
		form.Set("source_id", "1")
		form.Set("source_url", "https://mangadex.org/title/out-of-range")
		form.Set("status", "reading")
		form.Set(tc.field, tc.value)
		res := postForm(t, app, "/dashboard/trackers", form, nil)
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusBadRequest || string(body) != tc.want {
			t.Fatalf("%s=%s: expected 400 %q, got %d %q", tc.field, tc.value, tc.want, res.StatusCode, body)
		}
	}
	if got := countTrackers(t, db); got != 0 {
		t.Fatalf("refused creates must write nothing, found %d trackers", got)
	}

	form := url.Values{}
	form.Set("title", "Decimal Chapter")
	form.Set("source_id", "1")
	form.Set("source_url", "https://mangadex.org/title/decimal-chapter")
	form.Set("status", "reading")
	form.Set("last_read_chapter", "1789")
	form.Set("latest_known_chapter", "1829.3")
	if res := postForm(t, app, "/dashboard/trackers", form, nil); res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("in-range create: expected 200, got %d (body: %s)", res.StatusCode, body)
	}
	if got := countTrackers(t, db); got != 1 {
		t.Fatalf("expected the in-range tracker to be created, found %d trackers", got)
	}
}

// On the edit form, a chapter left as stored is not checked again — whether
// the modal sent its original alongside or not — while one the user typed is.
func TestDashboardEditChecksOnlyChangedChapters(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (1, 'Odd Numbering', 1, 'https://mangadex.org/title/odd-numbering', 'reading', 12, 250000)
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, _ := result.LastInsertId()
	path := "/dashboard/trackers/" + strconv.FormatInt(id, 10)

	edit := func(title string, fields map[string]string) (int, string) {
		t.Helper()
		form := url.Values{}
		form.Set("title", title)
		form.Set("source_id", "1")
		form.Set("source_url", "https://mangadex.org/title/odd-numbering")
		form.Set("status", "reading")
		for key, value := range fields {
			form.Set(key, value)
		}
		res := postForm(t, app, path, form, nil)
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}

	if status, body := edit("Renamed Once", map[string]string{
		"latest_known_chapter": "250000", "latest_known_chapter_original": "250000",
		"last_read_chapter": "12", "last_read_chapter_original": "12",
	}); status != http.StatusOK {
		t.Fatalf("title edit with originals: expected 200, got %d %q", status, body)
	}
	if status, body := edit("Renamed Twice", map[string]string{
		"latest_known_chapter": "250000", "last_read_chapter": "12",
	}); status != http.StatusOK {
		t.Fatalf("title edit without originals: expected 200, got %d %q", status, body)
	}

	status, body := edit("Refused Rename", map[string]string{
		"latest_known_chapter": "250000", "latest_known_chapter_original": "250000",
		"last_read_chapter": "-1", "last_read_chapter_original": "12",
	})
	if status != http.StatusBadRequest || body != "Last read chapter must be between 0 and 100000" {
		t.Fatalf("edited out-of-range chapter: expected 400, got %d %q", status, body)
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM trackers WHERE id = ?`, id).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "Renamed Twice" {
		t.Fatalf("the refused edit must write nothing, title is %q", title)
	}
	if lastRead, latest := storedChapters(t, db, id); lastRead.Float64 != 12 || latest.Float64 != 250000 {
		t.Fatalf("chapters changed: %v / %v", lastRead, latest)
	}
}
