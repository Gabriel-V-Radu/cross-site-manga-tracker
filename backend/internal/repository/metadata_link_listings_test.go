package repository_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

// seedLinkedTracker stores a tracker on MangaDex (source 1) with its mirror
// row and a MangaFire (source 2) alternate, the shape a linked tracker has on
// the deployed database.
func seedLinkedTracker(t *testing.T, db *sql.DB, title string) int64 {
	t.Helper()
	id := seedMetadataTracker(t, db, title, 1, 2)
	if _, err := db.Exec(`INSERT INTO tracker_sources (tracker_id, source_id, source_url) VALUES (?, 1, ?), (?, 2, ?)`,
		id, "https://mangadex.org/title/"+title, id, "https://mangafire.to/title/"+title); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	return id
}

func trackerIDsOf(links []repository.MetadataLink) map[int64]int {
	counts := map[int64]int{}
	for _, link := range links {
		counts[link.TrackerID]++
	}
	return counts
}

// TestMetadataLinkStampsCarryNoPayloads pins the cheap listing the reconcile
// pass runs every few minutes: every link of every tracker, when its site's
// metadata was stored, and never the metadata itself.
func TestMetadataLinkStampsCarryNoPayloads(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewMetadataRepository(db)
	ctx := context.Background()
	with := seedLinkedTracker(t, db, "with")
	without := seedLinkedTracker(t, db, "without")

	stamp := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	if err := repo.UpsertSourceMetadata(ctx, 2, "https://mangafire.to/title/with", `{"type":"manhwa"}`, stamp); err != nil {
		t.Fatal(err)
	}

	stamps, err := repo.ListMetadataLinkStamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts := trackerIDsOf(stamps); len(stamps) != 4 || counts[with] != 2 || counts[without] != 2 {
		t.Fatalf("expected two links per tracker (the mirror folded into the primary), got %+v", stamps)
	}
	stamped := 0
	for _, link := range stamps {
		if link.Payload != "" {
			t.Fatalf("a stamp carries a payload: %+v", link)
		}
		if link.SourceKey == "" || link.SourceName == "" {
			t.Fatalf("a stamp lost its site: %+v", link)
		}
		if link.FetchedAt != nil {
			stamped++
			if link.TrackerID != with || link.SourceID != 2 || !link.FetchedAt.Equal(stamp) {
				t.Fatalf("the stamp is on the wrong link: %+v", link)
			}
		}
	}
	if stamped != 1 {
		t.Fatalf("expected one stamped link, got %d", stamped)
	}

	// The full listing agrees with the stamps link for link, and adds the
	// payload: a view built from it is signed with the same inputs.
	full, err := repo.ListMetadataLinks(ctx, []int64{with, without})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != len(stamps) {
		t.Fatalf("the full listing has %d links, the stamps %d", len(full), len(stamps))
	}
	for index, link := range full {
		stampLink := stamps[index]
		if link.TrackerID != stampLink.TrackerID || link.SourceID != stampLink.SourceID || link.SourceURL != stampLink.SourceURL ||
			(link.FetchedAt == nil) != (stampLink.FetchedAt == nil) {
			t.Fatalf("link %d differs between the listings: %+v vs %+v", index, link, stampLink)
		}
		if (link.Payload != "") != (link.FetchedAt != nil) {
			t.Fatalf("payload and stamp disagree on %+v", link)
		}
	}
}

// TestMetadataLinksAreListedOnlyForTheTrackersAsked: the payload-carrying
// listing is scoped, and asking for nobody asks the database nothing.
func TestMetadataLinksAreListedOnlyForTheTrackersAsked(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewMetadataRepository(db)
	ctx := context.Background()
	first := seedLinkedTracker(t, db, "first")
	second := seedLinkedTracker(t, db, "second")
	for _, title := range []string{"first", "second"} {
		if err := repo.UpsertSourceMetadata(ctx, 1, "https://mangadex.org/title/"+title, `{"type":"manga"}`, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	links, err := repo.ListMetadataLinks(ctx, []int64{second})
	if err != nil {
		t.Fatal(err)
	}
	if counts := trackerIDsOf(links); len(links) != 2 || counts[second] != 2 || counts[first] != 0 {
		t.Fatalf("expected only the second tracker's two links, got %+v", links)
	}

	none, err := repo.ListMetadataLinks(ctx, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("no trackers asked for = %+v, %v", none, err)
	}

	// More trackers than one statement's IN list takes: the batches must add
	// up to every tracker asked for, each once.
	ids := []int64{first, second}
	for index := 0; index < 1200; index++ {
		ids = append(ids, int64(100000+index))
	}
	many, err := repo.ListMetadataLinks(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if counts := trackerIDsOf(many); len(many) != 4 || counts[first] != 2 || counts[second] != 2 {
		t.Fatalf("a batched listing returned %+v", many)
	}
}

// TestMetadataBackfillLinksAreTheTrackersWithNoMetadataAtAll pins what the
// backfill is handed: every link of a tracker none of whose sites has stored
// metadata, and nothing of a tracker that has any — through its primary, its
// mirror row or an alternate, and however the URL is cased or padded.
func TestMetadataBackfillLinksAreTheTrackersWithNoMetadataAtAll(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewMetadataRepository(db)
	ctx := context.Background()

	bare := seedLinkedTracker(t, db, "bare")
	viaAlternate := seedLinkedTracker(t, db, "via-alternate")
	viaPrimary := seedMetadataTracker(t, db, "via-primary", 1, 2) // no tracker_sources rows at all
	// The same series tracked twice (two profiles do this): one stored row
	// answers for both.
	sharedA := seedMetadataTracker(t, db, "shared", 1, 2)
	result, err := db.Exec(`
		INSERT INTO trackers (profile_id, title, source_id, source_url, status)
		VALUES (1, 'shared again', 1, ' HTTPS://MangaDex.org/title/shared ', 'reading')`)
	if err != nil {
		t.Fatal(err)
	}
	sharedB, _ := result.LastInsertId()

	all, err := repo.ListMetadataBackfillLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts := trackerIDsOf(all); counts[bare] != 2 || counts[viaAlternate] != 2 || counts[viaPrimary] != 1 || counts[sharedA] != 1 || counts[sharedB] != 1 || len(all) != 7 {
		t.Fatalf("with nothing stored every tracker's links are listed, got %+v", all)
	}
	for index, link := range all {
		if link.Payload != "" || link.FetchedAt != nil || link.SourceKey == "" {
			t.Fatalf("a backfill link carries metadata or lost its site: %+v", link)
		}
		if index > 0 && all[index-1].TrackerID > link.TrackerID {
			t.Fatalf("backfill links are not in tracker order: %+v", all)
		}
	}

	now := time.Now()
	for _, write := range []struct {
		sourceID int64
		url      string
	}{
		{2, "https://mangafire.to/title/via-alternate"},
		{1, "https://mangadex.org/title/via-primary"},
		{1, "https://mangadex.org/title/shared"},
	} {
		if err := repo.UpsertSourceMetadata(ctx, write.sourceID, write.url, `{"type":"manga"}`, now); err != nil {
			t.Fatal(err)
		}
	}

	left, err := repo.ListMetadataBackfillLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts := trackerIDsOf(left); len(left) != 2 || counts[bare] != 2 {
		t.Fatalf("expected only the tracker with no metadata anywhere, got %+v", left)
	}

	if err := repo.UpsertSourceMetadata(ctx, 1, "https://mangadex.org/title/bare", `{"type":"manga"}`, now); err != nil {
		t.Fatal(err)
	}
	if left, err = repo.ListMetadataBackfillLinks(ctx); err != nil || len(left) != 0 {
		t.Fatalf("a library with metadata everywhere leaves the backfill %+v, %v", left, err)
	}
}

// TestMetadataBackfillListingNeverReadsTheStoredMetadata pins the plan of the
// query the loop runs every minute. Whether a tracker has metadata must be
// answered from source_metadata's primary-key index — a covering lookup per
// link — and not by reading the table, whose rows are the payloads this
// listing exists to avoid.
func TestMetadataBackfillListingNeverReadsTheStoredMetadata(t *testing.T) {
	db := openPollingTestDB(t)
	seedLinkedTracker(t, db, "planned")

	plan := explainQueryPlan(t, db, repository.MetadataBackfillLinksQueryForTest)
	lookups := 0
	for _, detail := range plan {
		if !strings.Contains(detail, " sm ") && !strings.HasSuffix(detail, " sm") {
			continue
		}
		if !strings.Contains(detail, "SEARCH sm USING COVERING INDEX sqlite_autoindex_source_metadata_1 (source_id=? AND source_url=?)") {
			t.Fatalf("source_metadata is not reached through a covering primary-key lookup:\n%s", strings.Join(plan, "\n"))
		}
		lookups++
	}
	if lookups != 2 {
		t.Fatalf("expected one lookup for primaries and one for linked sites, got %d:\n%s", lookups, strings.Join(plan, "\n"))
	}
}

// TestUpsertSourceMetadataRefusesAnEmptyPayload: a stored row means "this
// site has metadata", which the backfill listing takes on the row's existence
// alone.
func TestUpsertSourceMetadataRefusesAnEmptyPayload(t *testing.T) {
	db := openPollingTestDB(t)
	repo := repository.NewMetadataRepository(db)
	if err := repo.UpsertSourceMetadata(context.Background(), 1, "https://mangadex.org/title/x", "  ", time.Now()); err == nil {
		t.Fatal("an empty payload was stored")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM source_metadata`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows = %d, %v", count, err)
	}
}
