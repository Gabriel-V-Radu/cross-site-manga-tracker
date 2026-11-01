package handlers_test

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The dashboard's behaviour lives mostly in markup and script that no Go test
// executes, so these pin the markup the scripts and the browser rely on: what a
// click on a caption reaches, which menus refresh themselves, what the edit
// form sends back, and what the list header keeps on a single page.

var (
	markupLabelPattern     = regexp.MustCompile(`(?is)<label\b([^>]*)>(.*?)</label>`)
	markupLabelForPattern  = regexp.MustCompile(`(?i)\sfor\s*=`)
	markupLabelablePattern = regexp.MustCompile(`(?i)<(input|select|textarea|button|meter|output|progress)\b[^>]*>`)
	markupHiddenPattern    = regexp.MustCompile(`(?i)\btype\s*=\s*"hidden"`)
	markupDetailsPattern   = regexp.MustCompile(`(?i)<details\b`)
)

// labelsForwardingToAnotherControl returns every <label> without a "for" that
// holds more than one labelable control, or a <details>. Such a label labels
// its first control, so a click on its caption — or on any of its own empty
// space — is delivered there: the Sites and Tags captions ticked the first
// checkbox in their closed dropdowns, and the tag-icon caption reset the
// picked icon to None. A nested label ends the match early, which still leaves
// the outer label's opening and first control inside it, so the old Sites
// wrapper is caught.
func labelsForwardingToAnotherControl(html string) []string {
	var offenders []string
	for _, match := range markupLabelPattern.FindAllStringSubmatch(html, -1) {
		if markupLabelForPattern.MatchString(match[1]) {
			continue
		}
		body := match[2]
		controls := 0
		for _, tag := range markupLabelablePattern.FindAllStringSubmatch(body, -1) {
			if strings.EqualFold(tag[1], "input") && markupHiddenPattern.MatchString(tag[0]) {
				continue
			}
			controls++
		}
		if controls > 1 || markupDetailsPattern.MatchString(body) {
			offenders = append(offenders, markupSnippet(match[0], 240))
		}
	}
	return offenders
}

func markupSnippet(text string, limit int) string {
	compact := strings.Join(strings.Fields(text), " ")
	if len(compact) > limit {
		return compact[:limit] + "…"
	}
	return compact
}

func getMarkup(t *testing.T, app *fiber.App, path string) string {
	t.Helper()

	res, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), 10000)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d (body: %s)", path, res.StatusCode, markupSnippet(string(body), 400))
	}
	return string(body)
}

func seedMarkupTracker(t *testing.T, db *sql.DB, title string, lastRead, latest any) int64 {
	t.Helper()

	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, 1, title, 1, "https://mangadex.org/title/"+strings.ReplaceAll(strings.ToLower(title), " ", "-"), "reading", lastRead, latest)
	if err != nil {
		t.Fatalf("seed tracker %q: %v", title, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tracker id: %v", err)
	}
	return id
}

// TestLabelScannerCatchesACaptionedGroup proves the check below fails on the
// markup it exists for rather than passing vacuously.
func TestLabelScannerCatchesACaptionedGroup(t *testing.T) {
	cases := []struct {
		name   string
		markup string
		bad    bool
	}{
		{
			name: "dropdown of checkboxes under a bare label",
			markup: `<label> Sites <details id="filter-sites-dropdown"><summary>0</summary><div>
				<label class="filter-multi-select__option"><input type="checkbox" name="sites" value="1"><span>A</span></label>
				</div></details></label>`,
			bad: true,
		},
		{
			name: "icon buttons under a bare label",
			markup: `<label> Icon (optional) <div class="tracker-tag-icon-picker">
				<button type="button" data-menu-icon="">None</button><button type="button" data-menu-icon="star">*</button>
				</div><input type="hidden" name="icon_key" value=""></label>`,
			bad: true,
		},
		{
			name:   "input and button pinned with for",
			markup: `<label for="q"> Search <input type="text" id="q" name="q"><button type="button">x</button></label>`,
			bad:    false,
		},
		{
			name:   "one control and a hidden input",
			markup: `<label> Title <input type="text" name="title"><input type="hidden" name="x" value=""></label>`,
			bad:    false,
		},
		{
			name:   "captioned group that is not a label",
			markup: `<div class="field-group"> Tags <details><summary>0</summary><label><input type="checkbox" name="tags" value="a"></label></details></div>`,
			bad:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(labelsForwardingToAnotherControl(tc.markup)) > 0
			if got != tc.bad {
				t.Fatalf("flagged=%v, want %v for %s", got, tc.bad, tc.markup)
			}
		})
	}
}

func TestDashboardLabelsForwardCaptionClicksOnlyToTheirOwnControl(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	if _, err := db.Exec(`INSERT INTO custom_tags (profile_id, name) VALUES (?, ?), (?, ?)`, 1, "Fav", 1, "Isekai"); err != nil {
		t.Fatalf("seed tags: %v", err)
	}
	trackerID := seedMarkupTracker(t, db, "Label Audit Tracker", 3.0, 5.0)

	for _, path := range []string{
		"/dashboard?profile=profile1",
		"/dashboard/profile/menu?profile=profile1",
		"/dashboard/profile/filter-tags?profile=profile1",
		"/dashboard/trackers/new?profile=profile1",
		"/dashboard/trackers/" + strconv.FormatInt(trackerID, 10) + "/edit?profile=profile1",
		"/dashboard/trackers?status=all&profile=profile1",
	} {
		html := getMarkup(t, app, path)
		if offenders := labelsForwardingToAnotherControl(html); len(offenders) > 0 {
			t.Errorf("%s has labels that forward a caption click to their first control (use a .field-group, or pin the label with for=):\n%s", path, strings.Join(offenders, "\n"))
		}
	}
}

// The site list is global and only changes with a reload, so the page renders
// the menu once, with the selection from the URL. A self-refresh on every
// trackersChanged (cover refreshes included) put the checkboxes back to an
// older selection when a response landed between two quick ticks.
func TestDashboardSitesMenuIsRenderedOnceWithThePage(t *testing.T) {
	_, app, cleanup := setupTestApp(t)
	defer cleanup()

	html := getMarkup(t, app, "/dashboard?profile=profile1&sites=2")

	if !strings.Contains(html, `name="sites" value="2" checked`) {
		t.Fatalf("expected the page to render site 2 ticked from ?sites=2")
	}
	if strings.Contains(html, `name="sites" value="1" checked`) {
		t.Fatalf("expected site 1 to stay unticked")
	}
	if strings.Contains(html, "/dashboard/profile/filter-linked-sites") {
		t.Fatalf("expected the sites menu not to re-fetch itself")
	}
}

// The edit form sends back the chapter numbers it opened with next to the
// editable ones, so the save can tell a number the user changed from one the
// poller advanced while the form was open. The two must render from the same
// expression: an untouched field has to submit exactly the original's string.
func TestEditTrackerModalSendsBackTheChaptersItOpenedWith(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	cases := []struct {
		name     string
		lastRead any
		latest   any
		wantRead string
		wantNext string
	}{
		{name: "Fractional Chapters", lastRead: 7.5, latest: 12.25, wantRead: "7.5", wantNext: "12.25"},
		{name: "Whole Chapters", lastRead: 40.0, latest: 41.0, wantRead: "40", wantNext: "41"},
		{name: "No Chapters", lastRead: nil, latest: nil, wantRead: "", wantNext: ""},
	}

	inputValue := func(html, name string) (string, bool) {
		pattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`)
		match := pattern.FindStringSubmatch(html)
		if match == nil {
			return "", false
		}
		return match[1], true
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := seedMarkupTracker(t, db, tc.name, tc.lastRead, tc.latest)
			html := getMarkup(t, app, "/dashboard/trackers/"+strconv.FormatInt(id, 10)+"/edit?profile=profile1")

			for _, field := range []struct{ name, want string }{
				{"last_read_chapter", tc.wantRead},
				{"latest_known_chapter", tc.wantNext},
			} {
				visible, ok := inputValue(html, field.name)
				if !ok {
					t.Fatalf("expected the edit form to render %s", field.name)
				}
				original, ok := inputValue(html, field.name+"_original")
				if !ok {
					t.Fatalf("expected the edit form to render %s_original", field.name)
				}
				if visible != field.want || original != visible {
					t.Fatalf("%s: visible %q, original %q, want both %q", field.name, visible, original, field.want)
				}
			}
		})
	}

	newForm := getMarkup(t, app, "/dashboard/trackers/new?profile=profile1")
	if strings.Contains(newForm, "_original") {
		t.Fatalf("expected the new-tracker form to carry no original chapter values")
	}
}

// "keyup changed" only compares the typed text, so each title search also
// listens to its own site select: a new site used to leave the previous site's
// results, or "Select a source first", on screen until the title was edited.
func TestTrackerFormSearchesRerunWhenTheirSiteChanges(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	newForm := getMarkup(t, app, "/dashboard/trackers/new?profile=profile1")
	if !strings.Contains(newForm, `id="tracker-source-select"`) || !strings.Contains(newForm, "change from:#tracker-source-select") {
		t.Fatalf("expected the title search to re-run when the Source select changes")
	}

	id := seedMarkupTracker(t, db, "Search Trigger Tracker", 1.0, 2.0)
	editForm := getMarkup(t, app, "/dashboard/trackers/"+strconv.FormatInt(id, 10)+"/edit?profile=profile1")
	if !strings.Contains(editForm, `id="linked-source-id"`) || !strings.Contains(editForm, "change from:#linked-source-id") {
		t.Fatalf("expected the additional-site search to re-run when Site to Add changes")
	}
}

// The tag menu now submits a rename that only changes capitalisation (it used
// to drop it client-side), so the server has to accept it: the name is unique
// without regard to case, but the update is to the same row.
func TestRenameTagFixesOnlyItsCapitalisation(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	result, err := db.Exec(`INSERT INTO custom_tags (profile_id, name) VALUES (?, ?)`, 1, "isekai")
	if err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	tagID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tag id: %v", err)
	}

	form := url.Values{}
	form.Set("tag_id", strconv.FormatInt(tagID, 10))
	form.Set("tag_name", "Isekai")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/profile/tags/rename?profile=profile1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("rename tag request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200, got %d (body: %s)", res.StatusCode, body)
	}

	var stored string
	if err := db.QueryRow(`SELECT name FROM custom_tags WHERE id = ?`, tagID).Scan(&stored); err != nil {
		t.Fatalf("read tag: %v", err)
	}
	if stored != "Isekai" {
		t.Fatalf("expected the tag to be renamed to %q, got %q", "Isekai", stored)
	}
}
