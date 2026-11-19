package repository_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// rawColumn reads a tracker column as the text SQLite stores. Concatenating
// drops the column's DATETIME type, so the driver does not parse it.
func rawColumn(t *testing.T, db *sql.DB, table string, column string, id int64) sql.NullString {
	t.Helper()
	var raw sql.NullString
	if err := db.QueryRow(`SELECT `+column+` || '' FROM `+table+` WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read %s.%s of %d: %v", table, column, id, err)
	}
	return raw
}

// ageUpdatedAt backdates a row's updated_at, so a statement that restamps it
// can be told from one that left it alone: both write within the second.
const agedStamp = "2020-01-01 00:00:00"

func ageUpdatedAt(t *testing.T, db *sql.DB, table string, where string, args ...any) {
	t.Helper()
	if _, err := db.Exec(`UPDATE `+table+` SET updated_at = '`+agedStamp+`' WHERE `+where, args...); err != nil {
		t.Fatalf("age %s.updated_at: %v", table, err)
	}
}

// A time is stored as its text, and that text is what the columns are sorted
// and compared by. Bound as it came — in the server's zone, with the
// monotonic reading time.Now() carries — the same instant was a different
// text from one bind to the next: stored out of order beside the UTC rows,
// and an edit form saved without touching anything saw the timestamp it had
// just read back as changed.
func TestTimesAreBoundInUTCWithoutAMonotonicReading(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()

	madrid := time.FixedZone("CEST", 2*60*60)
	released := time.Date(2026, 11, 19, 14, 3, 22, 0, madrid)
	checked := time.Now() // the local zone, and a monotonic reading
	chapter := 10.0
	created, err := repo.Create(ctx, &models.Tracker{
		ProfileID: 1, Title: "Timed", SourceID: 1, SourceURL: "https://mangadex.org/title/timed", Status: "reading",
		LatestKnownChapter: &chapter, LatestReleaseAt: &released, LastCheckedAt: &checked,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if raw := rawColumn(t, db, "trackers", "latest_release_at", created.ID); raw.String != "2026-11-19 12:03:22 +0000 UTC" {
		t.Fatalf("a release dated in another zone was stored as %q, want the instant in UTC", raw.String)
	}
	rawChecked := rawColumn(t, db, "trackers", "last_checked_at", created.ID).String
	if !strings.HasSuffix(rawChecked, " +0000 UTC") || strings.Contains(rawChecked, "m=") {
		t.Fatalf("time.Now() was stored as %q, want UTC and no monotonic reading", rawChecked)
	}
	if !created.LastCheckedAt.Equal(checked) || !created.LatestReleaseAt.Equal(released) {
		t.Fatalf("the instants did not survive: checked %v (want %v), released %v (want %v)",
			created.LastCheckedAt, checked, created.LatestReleaseAt, released)
	}

	// Saving the tracker exactly as it was read back changes nothing, in
	// whichever zone the caller happens to hold the same instants.
	ageUpdatedAt(t, db, "trackers", "id = ?", created.ID)
	for name, zone := range map[string]*time.Location{"as read": nil, "in another zone": time.FixedZone("JST", 9*60*60)} {
		again := *created
		if zone != nil {
			releasedThere, checkedThere := created.LatestReleaseAt.In(zone), created.LastCheckedAt.In(zone)
			again.LatestReleaseAt, again.LastCheckedAt = &releasedThere, &checkedThere
		}
		if _, err := repo.Update(ctx, 1, created.ID, &again); err != nil {
			t.Fatalf("%s: update: %v", name, err)
		}
		if raw := rawColumn(t, db, "trackers", "updated_at", created.ID); raw.String != agedStamp {
			t.Fatalf("%s: an update that changed nothing restamped the tracker (updated_at %q)", name, raw.String)
		}
	}
}

// Moving a tracker's primary through Update takes the old primary's mirror
// row with it. Left behind, the row no longer matched the primary and so
// counted as an alternate: the tracker went on being polled on the link it
// had just been taken off — in good standing, the row still carrying the old
// primary's in_step — when the usual reason to repoint is that the old link
// was the wrong series.
func TestUpdateRemovesTheMirrorOfThePrimaryItReplaces(t *testing.T) {
	ctx := context.Background()
	alternatesOf := func(t *testing.T, repo *repository.TrackerRepository, trackerID int64) []repository.TrackerSourceRef {
		t.Helper()
		trackers, err := repo.ListForPolling(ctx)
		if err != nil {
			t.Fatalf("list for polling: %v", err)
		}
		for _, tracker := range trackers {
			if tracker.ID == trackerID {
				return tracker.AlternateSources
			}
		}
		t.Fatalf("tracker %d not listed", trackerID)
		return nil
	}

	t.Run("to another series on the same site", func(t *testing.T) {
		db := openPollingTestDB(t)
		repo := repository.NewTrackerRepository(db)
		created := seedSaveTracker(t, repo)
		if _, err := db.Exec(`UPDATE tracker_sources SET in_step = 1 WHERE tracker_id = ?`, created.ID); err != nil {
			t.Fatal(err)
		}

		moved := *created
		moved.SourceURL = "https://mangadex.org/title/the-right-one"
		if _, err := repo.Update(ctx, 1, created.ID, &moved); err != nil {
			t.Fatalf("update: %v", err)
		}
		links := linkRows(t, db, created.ID, 1)
		if len(links) != 1 || links[0].sourceURL != moved.SourceURL {
			t.Fatalf("links on the site after the move = %+v, want only the new primary's mirror", links)
		}
		if alternates := alternatesOf(t, repo, created.ID); len(alternates) != 0 {
			t.Fatalf("the replaced primary is still polled as an alternate: %+v", alternates)
		}
	})

	t.Run("to another site, with a reading pin on the old one", func(t *testing.T) {
		db := openPollingTestDB(t)
		repo := repository.NewTrackerRepository(db)
		created := seedSaveTracker(t, repo)
		if err := repo.UpsertTrackerSource(ctx, 1, created.ID, models.TrackerSource{SourceID: 3, SourceURL: "https://asurascans.com/comics/original"}); err != nil {
			t.Fatalf("link an alternate: %v", err)
		}
		oldSite := int64(1)
		if err := repo.SetReadingSource(ctx, 1, created.ID, &oldSite); err != nil {
			t.Fatalf("pin: %v", err)
		}

		moved := *created
		moved.SourceID, moved.SourceURL = 2, "https://mangafire.to/manga/original.abc"
		updated, err := repo.Update(ctx, 1, created.ID, &moved)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if got := len(linkRows(t, db, created.ID, 1)); got != 0 {
			t.Fatalf("the old site still has %d link rows", got)
		}
		if got := len(linkRows(t, db, created.ID, 2)); got != 1 {
			t.Fatalf("the new primary has %d mirror rows, want 1", got)
		}
		alternates := alternatesOf(t, repo, created.ID)
		if len(alternates) != 1 || alternates[0].SourceID != 3 {
			t.Fatalf("alternates after the move = %+v, want the one that was linked on purpose", alternates)
		}
		if updated.ReadingSourceID != nil {
			t.Fatalf("the reading pin still points at the site just unlinked: %d", *updated.ReadingSourceID)
		}
	})

	t.Run("an update that leaves the primary where it is keeps its row", func(t *testing.T) {
		db := openPollingTestDB(t)
		repo := repository.NewTrackerRepository(db)
		created := seedSaveTracker(t, repo)
		before := onlyLinkRow(t, db, created.ID, 1)

		renamed := *created
		renamed.Title = "Renamed"
		if _, err := repo.Update(ctx, 1, created.ID, &renamed); err != nil {
			t.Fatalf("update: %v", err)
		}
		if after := onlyLinkRow(t, db, created.ID, 1); after.id != before.id {
			t.Fatalf("a rename recreated the mirror row: id %d -> %d", before.id, after.id)
		}
	})
}

// An edit that sets the chapter by hand, or moves the primary, clears what
// the poller had noted about the number and the link it replaces; one that
// touches neither leaves it alone.
func TestEditClearsThePollersNotesAboutWhatItReplaces(t *testing.T) {
	ctx := context.Background()
	type notes struct{ pendingLower, pendingSeen, suppliedBy, feedMissed bool }
	read := func(t *testing.T, db *sql.DB, id int64) notes {
		t.Helper()
		var got notes
		if err := db.QueryRow(`
			SELECT pending_lower_chapter IS NOT NULL, pending_lower_first_seen_at IS NOT NULL,
			       latest_chapter_source_id IS NOT NULL, feed_missed_at IS NOT NULL
			FROM trackers WHERE id = ?
		`, id).Scan(&got.pendingLower, &got.pendingSeen, &got.suppliedBy, &got.feedMissed); err != nil {
			t.Fatalf("read the poller's notes: %v", err)
		}
		return got
	}
	chapter := func(value float64) *float64 { return &value }

	cases := []struct {
		name string
		edit func(tracker *models.Tracker)
		want notes
	}{
		{"a rename", func(tracker *models.Tracker) { tracker.Title = "Renamed" }, notes{true, true, true, true}},
		{"a chapter set by hand", func(tracker *models.Tracker) { tracker.LatestKnownChapter = chapter(12) }, notes{false, false, false, true}},
		{"a chapter cleared by hand", func(tracker *models.Tracker) { tracker.LatestKnownChapter = nil }, notes{false, false, false, true}},
		{"the primary moved to another series", func(tracker *models.Tracker) {
			tracker.SourceURL = "https://mangadex.org/title/another"
		}, notes{false, false, false, false}},
		{"the primary moved to another site", func(tracker *models.Tracker) {
			tracker.SourceID, tracker.SourceURL = 2, "https://mangafire.to/manga/original.abc"
		}, notes{false, false, false, false}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openPollingTestDB(t)
			repo := repository.NewTrackerRepository(db)
			created, err := repo.Create(ctx, &models.Tracker{
				ProfileID: 1, Title: "Original", SourceID: 1, SourceURL: "https://mangadex.org/title/original",
				Status: "reading", LatestKnownChapter: chapter(10),
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := db.Exec(`
				UPDATE trackers
				SET pending_lower_chapter = 9, pending_lower_first_seen_at = '2026-11-01 00:00:00',
				    latest_chapter_source_id = 2, feed_missed_at = '2026-11-10 00:00:00'
				WHERE id = ?
			`, created.ID); err != nil {
				t.Fatalf("seed the poller's notes: %v", err)
			}

			edited := *created
			testCase.edit(&edited)
			// Through the edit form's save, which shares the row update with Update.
			if _, err := repo.SaveTrackerEdit(ctx, 1, created.ID, &edited, repository.TrackerLinks{}); err != nil {
				t.Fatalf("save: %v", err)
			}
			if got := read(t, db, created.ID); got != testCase.want {
				t.Fatalf("after %s the notes kept are %+v, want %+v", testCase.name, got, testCase.want)
			}
		})
	}
}

// A link is one row however its URL is spelled, and a write that knows no
// item id keeps the one the row has. The single-link writers used to upsert on
// the table's unique key, the URL to the letter: another capital or a
// trailing space made a second row, and a NULL item id replaced a stored one.
func TestLinkingAgainKeepsOneRowAndItsItemID(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	created := seedSaveTracker(t, repo)

	itemID := "abc"
	const stored = "https://mangafire.to/manga/series.abc"
	if err := repo.UpsertTrackerSource(ctx, 1, created.ID, models.TrackerSource{SourceID: 2, SourceItemID: &itemID, SourceURL: stored}); err != nil {
		t.Fatalf("link: %v", err)
	}
	first := onlyLinkRow(t, db, created.ID, 2)
	ageUpdatedAt(t, db, "tracker_sources", "id = ?", first.id)

	// The same link again, exactly: nothing to write.
	if err := repo.UpsertTrackerSource(ctx, 1, created.ID, models.TrackerSource{SourceID: 2, SourceURL: stored}); err != nil {
		t.Fatalf("link again: %v", err)
	}
	if raw := rawColumn(t, db, "tracker_sources", "updated_at", first.id); raw.String != agedStamp {
		t.Fatalf("linking the same link again restamped the row (updated_at %q)", raw.String)
	}

	// And spelled another way, with no item id.
	const respelled = "https://MangaFire.to/manga/series.abc"
	if err := repo.UpsertTrackerSource(ctx, 1, created.ID, models.TrackerSource{SourceID: 2, SourceURL: "  " + respelled + " "}); err != nil {
		t.Fatalf("link respelled: %v", err)
	}
	again := onlyLinkRow(t, db, created.ID, 2)
	if again.id != first.id || again.sourceURL != respelled {
		t.Fatalf("the respelled link is row %d %q, want the first row %d taking the new spelling", again.id, again.sourceURL, first.id)
	}
	if got := rawColumn(t, db, "tracker_sources", "source_item_id", first.id); got.String != "abc" {
		t.Fatalf("a link written without an item id left %q, want the stored one kept", got.String)
	}

	// Another profile's tracker is still left alone.
	if err := repo.UpsertTrackerSource(ctx, 2, created.ID, models.TrackerSource{SourceID: 3, SourceURL: "https://asurascans.com/comics/series"}); err != nil {
		t.Fatalf("link through another profile: %v", err)
	}
	if got := len(linkRows(t, db, created.ID, 3)); got != 0 {
		t.Fatalf("another profile linked %d rows onto the tracker", got)
	}
}

// A poll writes what it learned and no more. It used to restamp the tracker's
// updated_at every time — "recently updated" was the order of the last cycle
// — and to rewrite the primary's mirror row on every answer, item id
// included, with NULL when the answer carried none.
func TestUpdatePollingStateWritesOnlyWhatThePollChanged(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()

	itemID := "md-1"
	chapter := 19.0
	created, err := repo.Create(ctx, &models.Tracker{
		ProfileID: 1, Title: "Polled", SourceID: 1, SourceItemID: &itemID, SourceURL: pollingSeedURL,
		Status: "reading", LatestKnownChapter: &chapter,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mirror := onlyLinkRow(t, db, created.ID, 1)
	age := func() {
		ageUpdatedAt(t, db, "trackers", "id = ?", created.ID)
		ageUpdatedAt(t, db, "tracker_sources", "id = ?", mirror.id)
	}
	poll := func(latest float64, snapshot float64, releasedAt *time.Time) {
		t.Helper()
		applied, err := repo.UpdatePollingState(ctx, repository.PollingUpdate{
			TrackerID:        created.ID,
			SnapshotSourceID: 1, SnapshotSourceURL: pollingSeedURL, SnapshotLatestKnownChapter: &snapshot,
			// The primary answered, with no item id of its own this time.
			SourceID: 1, CurrentSourceURL: pollingSeedURL, SourceURL: pollingSeedURL,
			LatestKnownChapter: &latest,
			LatestReleaseAt:    releasedAt,
			SourcePolls:        []repository.SourcePoll{{Primary: true, Answered: true, InStep: true}},
			CheckedAt:          time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC),
		})
		if err != nil || !applied {
			t.Fatalf("poll: applied=%v err=%v", applied, err)
		}
	}

	age()
	poll(19, 19, nil)
	if raw := rawColumn(t, db, "trackers", "updated_at", created.ID); raw.String != agedStamp {
		t.Fatalf("a poll that found nothing new restamped the tracker (updated_at %q)", raw.String)
	}
	if raw := rawColumn(t, db, "tracker_sources", "updated_at", mirror.id); raw.String != agedStamp {
		t.Fatalf("a poll that changed nothing about the mirror row restamped it (updated_at %q)", raw.String)
	}
	if got := rawColumn(t, db, "tracker_sources", "source_item_id", mirror.id); got.String != "md-1" {
		t.Fatalf("an answer without an item id left the mirror with %q, want the stored one", got.String)
	}
	if got := rawColumn(t, db, "trackers", "source_item_id", created.ID); got.String != "md-1" {
		t.Fatalf("an answer without an item id left the tracker with %q, want the stored one", got.String)
	}
	// What the poll did learn is still written: the check, and the site's turn.
	if raw := rawColumn(t, db, "trackers", "last_checked_at", created.ID); !strings.HasPrefix(raw.String, "2026-11-19 09:00:00") {
		t.Fatalf("last_checked_at = %q, want the poll's time", raw.String)
	}
	if raw := rawColumn(t, db, "tracker_sources", "last_polled_at", mirror.id); !strings.HasPrefix(raw.String, "2026-11-19 09:00:00") {
		t.Fatalf("the mirror's last_polled_at = %q, want the poll's time", raw.String)
	}

	// A new chapter is a change the reader sees.
	age()
	poll(20, 19, nil)
	if raw := rawColumn(t, db, "trackers", "updated_at", created.ID); raw.String == agedStamp {
		t.Fatal("a poll that found a new chapter did not restamp the tracker")
	}

	// So is a release date filled in for the chapter it has.
	age()
	releasedAt := time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC)
	poll(20, 20, &releasedAt)
	if raw := rawColumn(t, db, "trackers", "updated_at", created.ID); raw.String == agedStamp {
		t.Fatal("a poll that filled the release date did not restamp the tracker")
	}
	// And the same date reported again is not.
	age()
	poll(20, 20, &releasedAt)
	if raw := rawColumn(t, db, "trackers", "updated_at", created.ID); raw.String != agedStamp {
		t.Fatalf("a poll that repeated the stored release date restamped the tracker (updated_at %q)", raw.String)
	}
}

// What the reader types in the search box is text. A "%" in it is that
// character, not LIKE's wildcard: "100%" used to match every title with "100"
// in it, and "%" alone every title there is. (An underscore never reaches the
// statement — the search's own normalisation turns it into a space — but the
// pattern escapes it all the same, so that does not have to stay true.)
func TestListSearchTreatsLikeWildcardsAsText(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)

	createListingTracker(t, repo, "100% Orange Juice", "orange")
	createListingTracker(t, repo, "1001 Nights", "nights")
	createListingTracker(t, repo, "Re_Zero", "rezero")

	cases := map[string][]string{
		"100%":     {"100% Orange Juice"},
		"100":      {"100% Orange Juice", "1001 Nights"},
		"%":        {"100% Orange Juice"},
		"% orange": {"100% Orange Juice"},
		"re_zero":  {"Re_Zero"},
		"nights":   {"1001 Nights"},
	}
	for query, want := range cases {
		got := listTitles(t, repo, repository.TrackerListOptions{Query: query})
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("search %q found %v, want %v", query, got, want)
		}
	}
}

// A status filter matches whatever case it was written in. The value used to
// be bound as passed while the column only ever holds lower case, so
// "Reading" turned on the reading queue's own rule and found no tracker.
func TestListStatusFilterIgnoresCase(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	createListingTracker(t, repo, "Being Read", "reading")
	dropped := createListingTracker(t, repo, "Dropped", "dropped")
	if _, err := db.Exec(`UPDATE trackers SET status = 'dropped' WHERE id = ?`, dropped); err != nil {
		t.Fatal(err)
	}

	for _, statuses := range [][]string{{"reading"}, {"Reading"}, {" READING "}, {"reading", "Reading"}} {
		if got := listTitles(t, repo, repository.TrackerListOptions{Statuses: statuses}); len(got) != 1 || got[0] != "Being Read" {
			t.Errorf("statuses %q listed %v, want only the reading tracker", statuses, got)
		}
	}
	if got := listTitles(t, repo, repository.TrackerListOptions{Statuses: []string{"Dropped", "READING"}}); len(got) != 2 {
		t.Errorf("two statuses in mixed case listed %v, want both trackers", got)
	}
}

// The alternates of the trackers asked for, and of no others: a page of cards
// does not need the library's.
func TestListAlternateSourcesByTrackerIDs(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()

	link := func(trackerID int64, sourceID int64, url string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, ?, ?)`, trackerID, sourceID, url); err != nil {
			t.Fatalf("link: %v", err)
		}
	}
	shown := createListingTracker(t, repo, "Shown", "shown")
	alsoShown := createListingTracker(t, repo, "Also Shown", "also-shown")
	elsewhere := createListingTracker(t, repo, "On Another Page", "elsewhere")
	foreign := createListingTracker(t, repo, "Another Profile's", "foreign")
	if _, err := db.Exec(`UPDATE trackers SET profile_id = 2 WHERE id = ?`, foreign); err != nil {
		t.Fatal(err)
	}
	link(shown, 2, "https://mangafire.to/manga/shown.a")
	link(shown, 3, "https://asurascans.com/comics/shown")
	link(elsewhere, 2, "https://mangafire.to/manga/elsewhere.b")
	link(foreign, 2, "https://mangafire.to/manga/foreign.c")

	alternates, err := repo.ListAlternateSourcesByTrackerIDs(ctx, 1, []int64{shown, alsoShown, foreign, shown, 0})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(alternates) != 1 {
		t.Fatalf("alternates came back for trackers %v, want only the one asked for that has any", alternates)
	}
	// The primary's own mirror row (Create wrote one) is not an alternate.
	if got := alternates[shown]; len(got) != 2 || got[0].SourceURL == "https://mangadex.org/title/shown" || got[1].SourceURL == "https://mangadex.org/title/shown" {
		t.Fatalf("alternates of the shown tracker = %+v, want its two linked sites", got)
	}

	if none, err := repo.ListAlternateSourcesByTrackerIDs(ctx, 1, nil); err != nil || len(none) != 0 {
		t.Fatalf("asking for no trackers = %v, %v", none, err)
	}
}
