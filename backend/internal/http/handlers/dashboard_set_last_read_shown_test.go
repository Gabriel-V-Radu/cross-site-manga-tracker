package handlers_test

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// "Set last read" means "I have read up to the chapter this card shows". The
// handler used to write whatever latest chapter the database held at click
// time, so a card rendered on Ch. 100 and clicked after a poll had stored 102
// marked 101 and 102 read too. The button now sends the chapter it was
// rendered with, and that is what is written — unless it is unusable, in which
// case the stored latest is, as before.
func TestSetLastReadWritesTheChapterTheCardShowed(t *testing.T) {
	cases := []struct {
		name string
		// shown is the field's value; nil sends no field at all.
		shown *string
		want  float64
	}{
		{name: "the card's chapter, below the stored latest, is written", shown: shownValue("100"), want: 100},
		{name: "a decimal chapter is written as it is", shown: shownValue("100.5"), want: 100.5},
		{name: "the stored latest itself", shown: shownValue("102"), want: 102},
		{name: "a request without the field writes the stored latest", shown: nil, want: 102},
		{name: "an empty field writes the stored latest", shown: shownValue(""), want: 102},
		{name: "garbage writes the stored latest", shown: shownValue("chapter one hundred"), want: 102},
		{name: "not a number writes the stored latest", shown: shownValue("NaN"), want: 102},
		{name: "a negative number writes the stored latest", shown: shownValue("-5"), want: 102},
		{name: "a chapter above the stored latest is cut down to it", shown: shownValue("150"), want: 102},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db, app, cleanup := setupTestApp(t)
			defer cleanup()

			// Where the database stands after a poll moved the latest chapter
			// from the 100 the card was rendered with to 102.
			result, err := db.Exec(`
				INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
				VALUES (1, 'Stale Card', 1, 'https://mangadex.org/title/stale-card', 'reading', 99, 102)
			`)
			if err != nil {
				t.Fatalf("seed tracker: %v", err)
			}
			id, _ := result.LastInsertId()

			form := url.Values{}
			if testCase.shown != nil {
				form.Set("shown_latest_chapter", *testCase.shown)
			}
			res := postForm(t, app, "/dashboard/trackers/"+strconv.FormatInt(id, 10)+"/set-last-read", form, nil)
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, body)
			}

			var lastRead, latest sql.NullFloat64
			if err := db.QueryRow(`SELECT last_read_chapter, latest_known_chapter FROM trackers WHERE id = ?`, id).Scan(&lastRead, &latest); err != nil {
				t.Fatalf("load tracker: %v", err)
			}
			if !lastRead.Valid || lastRead.Float64 != testCase.want {
				t.Fatalf("last_read_chapter = %+v, want %v", lastRead, testCase.want)
			}
			if !latest.Valid || latest.Float64 != 102 {
				t.Fatalf("latest_known_chapter = %+v; setting last read must not touch it", latest)
			}

			// The card that comes back is the database's: it shows the chapters
			// still unread, and its button now carries the newer latest.
			html := string(body)
			wantLastRead := `data-last-read="` + strconv.FormatFloat(testCase.want, 'f', -1, 64) + `"`
			if !strings.Contains(html, wantLastRead) || !strings.Contains(html, `data-latest="102"`) {
				t.Fatalf("expected the re-rendered card to carry %s and the stored latest, got %s", wantLastRead, html)
			}
			if !strings.Contains(html, `hx-vals='{"shown_latest_chapter":"102"}'`) {
				t.Fatalf("expected the re-rendered button to send the chapter the card now shows, got %s", html)
			}
		})
	}
}

// The other half of the fix: every card's button names the chapter the card
// was rendered with. A card with no latest chapter sends nothing, and the
// handler then has nothing to write, as before.
func TestCardSetLastReadButtonSendsItsLatestChapter(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter)
		VALUES (1, 'Decimal Card', 1, 'https://mangadex.org/title/decimal-card', 'reading', 1829.3)
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	withChapter, _ := result.LastInsertId()
	result, err = db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'Chapterless Card', 1, 'https://mangadex.org/title/chapterless-card', 'reading')
	`)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	withoutChapter, _ := result.LastInsertId()

	status, html := fetchPage(t, app, "/dashboard/trackers/"+strconv.FormatInt(withChapter, 10)+"/card-fragment")
	if status != http.StatusOK {
		t.Fatalf("card fragment: status %d: %s", status, html)
	}
	if !strings.Contains(html, `hx-vals='{"shown_latest_chapter":"1829.3"}'`) {
		t.Fatalf("expected the button to send the card's latest chapter, got %s", html)
	}

	status, html = fetchPage(t, app, "/dashboard/trackers/"+strconv.FormatInt(withoutChapter, 10)+"/card-fragment")
	if status != http.StatusOK {
		t.Fatalf("card fragment: status %d: %s", status, html)
	}
	if strings.Contains(html, "shown_latest_chapter") {
		t.Fatalf("a card with no latest chapter has none to send, got %s", html)
	}

	res := postForm(t, app, "/dashboard/trackers/"+strconv.FormatInt(withoutChapter, 10)+"/set-last-read", url.Values{"shown_latest_chapter": {"12"}}, nil)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, body)
	}
	var lastRead sql.NullFloat64
	if err := db.QueryRow(`SELECT last_read_chapter FROM trackers WHERE id = ?`, withoutChapter).Scan(&lastRead); err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if lastRead.Valid {
		t.Fatalf("a tracker with no latest chapter has nothing to mark read, got %v", lastRead.Float64)
	}
}

// The info panel's script keeps its own copy for a minute and drops it when
// the card is saved. The response must not also be cacheable by the browser,
// or the refetch after an edit is answered with the copy from before it.
func TestTrackerInfoIsNotServedFromTheBrowserCache(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()
	id := seedInfoTracker(t, db, "Cached", 1, 2)

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/dashboard/trackers/"+strconv.FormatInt(id, 10)+"/info", nil), 10000)
	if err != nil {
		t.Fatalf("info request: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	cacheControl := res.Header.Get("Cache-Control")
	if !strings.Contains(cacheControl, "no-cache") || strings.Contains(cacheControl, "max-age") {
		t.Fatalf("Cache-Control = %q, want one that makes the browser ask every time", cacheControl)
	}
}

func shownValue(value string) *string { return &value }
