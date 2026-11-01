package repository_test

import (
	"context"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// createListingTracker creates a profile-1 tracker on MangaDex with the given
// title and returns its id.
func createListingTracker(t *testing.T, repo *repository.TrackerRepository, title string, slug string) int64 {
	t.Helper()

	created, err := repo.Create(context.Background(), &models.Tracker{
		ProfileID: 1,
		Title:     title,
		SourceID:  1,
		SourceURL: "https://mangadex.org/title/" + slug,
		Status:    "reading",
	})
	if err != nil {
		t.Fatalf("create tracker %q: %v", title, err)
	}
	return created.ID
}

func listedTitles(t *testing.T, repo *repository.TrackerRepository, options repository.TrackerListOptions) []string {
	t.Helper()

	trackers, err := repo.List(context.Background(), options)
	if err != nil {
		t.Fatalf("list trackers: %v", err)
	}
	total, err := repo.Count(context.Background(), options)
	if err != nil {
		t.Fatalf("count trackers: %v", err)
	}
	if total != len(trackers) {
		t.Fatalf("count %d disagrees with the %d trackers listed", total, len(trackers))
	}

	titles := make([]string, 0, len(trackers))
	for _, tracker := range trackers {
		titles = append(titles, tracker.Title)
	}
	return titles
}

// TestListFiltersByTagsWithAccentedCapitals covers the tag filter comparing
// a Go-lowered name ('época') with SQLite's ASCII-only LOWER(ct.name)
// ('Época'): a tag starting with an accented capital never matched a single
// tracker. The ASCII case still must not matter, as the names are unique by
// it.
func TestListFiltersByTagsWithAccentedCapitals(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)
	ctx := context.Background()

	tagged := createListingTracker(t, repo, "Tagged Series", "tagged")
	createListingTracker(t, repo, "Untagged Series", "untagged")

	epoca, err := repo.CreateProfileTag(ctx, 1, "Época", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	action, err := repo.CreateProfileTag(ctx, 1, "Action", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if err := repo.ReplaceTrackerTags(ctx, 1, tagged, []int64{epoca.ID, action.ID}); err != nil {
		t.Fatalf("tag tracker: %v", err)
	}

	cases := []struct {
		name string
		tags []string
		want []string
	}{
		{name: "an accented capital matches its stored name", tags: []string{"Época"}, want: []string{"Tagged Series"}},
		{name: "surrounding space is ignored", tags: []string{"  Época "}, want: []string{"Tagged Series"}},
		{name: "ASCII case is ignored", tags: []string{"action"}, want: []string{"Tagged Series"}},
		{name: "every selected tag must match", tags: []string{"Época", "ACTION"}, want: []string{"Tagged Series"}},
		{name: "an unknown tag matches nothing", tags: []string{"Ética"}, want: []string{}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := listedTitles(t, repo, repository.TrackerListOptions{ProfileID: 1, TagNames: testCase.tags})
			if len(got) != len(testCase.want) {
				t.Fatalf("expected %v, got %v", testCase.want, got)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("expected %v, got %v", testCase.want, got)
				}
			}
		})
	}
}

// TestListSearchesTitlesWithNonASCIICapitals covers the title search lowering
// the query with Unicode rules while SQLite's LOWER() left the column's
// non-ASCII capitals alone: 'ōkami' could never find 'Ōkami Kakushi'.
func TestListSearchesTitlesWithNonASCIICapitals(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewTrackerRepository(db)

	createListingTracker(t, repo, "Ōkami Kakushi", "okami")
	createListingTracker(t, repo, "Something Else", "else")

	for _, query := range []string{"ōkami", "ŌKAMI", "Ōkami kakushi", "kakushi"} {
		t.Run(query, func(t *testing.T) {
			got := listedTitles(t, repo, repository.TrackerListOptions{ProfileID: 1, Query: query})
			if len(got) != 1 || got[0] != "Ōkami Kakushi" {
				t.Fatalf("expected only 'Ōkami Kakushi' for %q, got %v", query, got)
			}
		})
	}
}
