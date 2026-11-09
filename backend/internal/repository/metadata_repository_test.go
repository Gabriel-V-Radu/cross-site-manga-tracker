package repository_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func seedMetadataTracker(t *testing.T, db *sql.DB, title string, lastRead, latest float64) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, last_read_chapter, latest_known_chapter)
		VALUES (1, ?, 1, ?, 'reading', ?, ?)
	`, title, "https://mangadex.org/title/"+title, lastRead, latest)
	if err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

func saveView(t *testing.T, repo *repository.MetadataRepository, trackerID int64, seriesType, status, genres string) {
	t.Helper()
	if err := repo.SaveTrackerMetadata(context.Background(), []repository.TrackerMetadataRow{{
		TrackerID: trackerID, SeriesType: seriesType, SeriesStatus: status, GenresJSON: genres,
		Payload: `{}`, Signature: "sig", UpdatedAt: time.Now(),
	}}); err != nil {
		t.Fatalf("save view: %v", err)
	}
}

func listTitles(t *testing.T, repo *repository.TrackerRepository, options repository.TrackerListOptions) []string {
	t.Helper()
	options.ProfileID = 1
	options.SortBy = "title"
	options.Order = "asc"
	items, err := repo.List(context.Background(), options)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	titles := make([]string, 0, len(items))
	for _, item := range items {
		titles = append(titles, item.Title)
	}
	count, err := repo.Count(context.Background(), options)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != len(titles) {
		t.Fatalf("Count = %d but List returned %d", count, len(titles))
	}
	return titles
}

func TestListFiltersBySeriesMetadata(t *testing.T) {
	db := openPollingTestDB(t)
	trackers := repository.NewTrackerRepository(db)
	views := repository.NewMetadataRepository(db)

	alpha := seedMetadataTracker(t, db, "alpha", 1, 10)
	beta := seedMetadataTracker(t, db, "beta", 1, 10)
	gamma := seedMetadataTracker(t, db, "gamma", 1, 10)
	seedMetadataTracker(t, db, "delta", 1, 10) // no metadata at all
	saveView(t, views, alpha, "manhwa", "ongoing", `["Action","Romance"]`)
	saveView(t, views, beta, "manhua", "completed", `["Action"]`)
	saveView(t, views, gamma, "manga", "ongoing", `["Romance","Comedy"]`)

	if got := listTitles(t, trackers, repository.TrackerListOptions{SeriesTypes: []string{"manhwa", "manhua"}}); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("types manhwa|manhua = %v", got)
	}
	if got := listTitles(t, trackers, repository.TrackerListOptions{Genres: []string{"Action", "Romance"}}); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("genres Action&Romance = %v", got)
	}
	if got := listTitles(t, trackers, repository.TrackerListOptions{SeriesStatuses: []string{"completed"}}); len(got) != 1 || got[0] != "beta" {
		t.Fatalf("status completed = %v", got)
	}
	if got := listTitles(t, trackers, repository.TrackerListOptions{}); len(got) != 4 {
		t.Fatalf("no filter = %v", got)
	}

	facets, err := views.ListMetadataFacets(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if facets.Genres["Action"] != 2 || facets.Genres["Romance"] != 2 || facets.Genres["Comedy"] != 1 {
		t.Fatalf("genre facets = %v", facets.Genres)
	}
	if facets.Types["manhwa"] != 1 || facets.Statuses["ongoing"] != 2 {
		t.Fatalf("type/status facets = %v / %v", facets.Types, facets.Statuses)
	}
}

func TestReadingKeepsCaughtUpTrackersWhenNarrowed(t *testing.T) {
	db := openPollingTestDB(t)
	trackers := repository.NewTrackerRepository(db)
	views := repository.NewMetadataRepository(db)

	behind := seedMetadataTracker(t, db, "behind", 5, 10)
	caughtUp := seedMetadataTracker(t, db, "caughtup", 10, 10)
	saveView(t, views, behind, "manhwa", "", `["Action"]`)
	saveView(t, views, caughtUp, "manhwa", "", `["Action"]`)

	reading := []string{"reading"}
	if got := listTitles(t, trackers, repository.TrackerListOptions{Statuses: reading}); len(got) != 1 || got[0] != "behind" {
		t.Fatalf("Reading alone must leave out the caught-up tracker, got %v", got)
	}
	narrowed := []repository.TrackerListOptions{
		{Statuses: reading, Query: "caught"},
		{Statuses: reading, Genres: []string{"Action"}},
		{Statuses: reading, SeriesTypes: []string{"manhwa"}},
		{Statuses: reading, SourceIDs: []int64{1}},
	}
	for _, options := range narrowed {
		got := listTitles(t, trackers, options)
		found := false
		for _, title := range got {
			if title == "caughtup" {
				found = true
			}
		}
		if !found {
			t.Fatalf("Reading plus a filter must include caught-up trackers: options %+v gave %v", options, got)
		}
	}
}

func TestSourceMetadataUpsertAndLinks(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewMetadataRepository(db)
	ctx := context.Background()
	id := seedMetadataTracker(t, db, "linked", 1, 2)
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, 1, ?), (?, 2, 'https://mangafire.to/title/x.y')`,
		id, " HTTPS://mangadex.org/title/linked ", id); err != nil {
		t.Fatal(err)
	}

	if got, err := repo.SourceMetadataFetchedAt(ctx, 2, "https://mangafire.to/title/x.y"); err != nil || got != nil {
		t.Fatalf("fetched_at before any write = %v, %v", got, err)
	}
	stamp := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if err := repo.UpsertSourceMetadata(ctx, 2, " https://MangaFire.to/title/x.y", `{"type":"manhwa"}`, stamp); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.SourceMetadataFetchedAt(ctx, 2, "https://mangafire.to/title/x.y"); err != nil || got == nil || !got.Equal(stamp) {
		t.Fatalf("fetched_at after write = %v, %v", got, err)
	}

	links, err := repo.ListMetadataLinks(ctx, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("expected the primary (its mirror folded in) and the alternate, got %+v", links)
	}
	withPayload := 0
	for _, link := range links {
		if link.Payload != "" {
			withPayload++
			if link.SourceID != 2 || link.FetchedAt == nil {
				t.Fatalf("payload on the wrong link: %+v", link)
			}
		}
	}
	if withPayload != 1 {
		t.Fatalf("expected one link with stored metadata, got %d", withPayload)
	}
}

func TestUpdatePollingStateStampsFoundAtOnlyOnAdvance(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()
	id := seedPollingTracker(t, db, 10)

	foundAt := func() *time.Time {
		var value sql.NullTime
		if err := db.QueryRow(`SELECT latest_chapter_found_at FROM trackers WHERE id = ?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if !value.Valid {
			return nil
		}
		return &value.Time
	}
	poll := func(snapshot, latest float64, at time.Time) {
		t.Helper()
		snapshotValue, latestValue := snapshot, latest
		applied, err := repo.UpdatePollingState(ctx, repository.PollingUpdate{
			TrackerID: id, SnapshotSourceID: 1, SnapshotSourceURL: pollingSeedURL,
			SnapshotLatestKnownChapter: &snapshotValue, LatestKnownChapter: &latestValue, CheckedAt: at,
		})
		if err != nil || !applied {
			t.Fatalf("poll %v->%v: applied=%v err=%v", snapshot, latest, applied, err)
		}
	}

	first := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	poll(10, 10, first)
	if foundAt() != nil {
		t.Fatal("a confirmation is not a find")
	}
	poll(10, 11, first.Add(time.Hour))
	if got := foundAt(); got == nil || !got.Equal(first.Add(time.Hour)) {
		t.Fatalf("an advance must stamp the find, got %v", got)
	}
	poll(11, 11, first.Add(2*time.Hour))
	if got := foundAt(); got == nil || !got.Equal(first.Add(time.Hour)) {
		t.Fatalf("a confirmation must keep the find, got %v", got)
	}
	poll(11, 10.5, first.Add(3*time.Hour))
	if foundAt() != nil {
		t.Fatal("a correction down must clear the find")
	}
}

func TestLatestChapterSortRanksALateFindOnTop(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	if _, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status, latest_known_chapter, latest_release_at, latest_chapter_found_at)
		VALUES
			(1, 'recent', 1, 'https://mangadex.org/title/recent', 'reading', 5, '2026-11-07 10:00:00', NULL),
			(1, 'latefind', 1, 'https://mangadex.org/title/latefind', 'reading', 9, '2026-10-01 10:00:00', '2026-11-08 08:00:00'),
			(1, 'old', 1, 'https://mangadex.org/title/old', 'reading', 3, '2026-09-01 10:00:00', NULL)
	`); err != nil {
		t.Fatal(err)
	}
	items, err := repo.List(context.Background(), repository.TrackerListOptions{ProfileID: 1, SortBy: "latest_known_chapter", Order: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Title != "latefind" || items[1].Title != "recent" || items[2].Title != "old" {
		titles := []string{}
		for _, item := range items {
			titles = append(titles, item.Title)
		}
		t.Fatalf("order = %v, want latefind, recent, old", titles)
	}
	if items[0].LatestChapterFoundAt == nil {
		t.Fatal("List must read latest_chapter_found_at")
	}
}
