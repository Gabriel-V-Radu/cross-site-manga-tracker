package connectors

import (
	"context"
	"sort"
	"strings"
	"time"
)

// UpdateFeed is implemented by connectors whose site lists the series it
// updated most recently in a single request: a home page's "latest updates",
// or an API's newest-chapters listing. One such request says what a poll of
// every tracker linked to the site would otherwise ask series by series, so
// the poller reads the feeds every few minutes and resolves only the trackers
// a feed shows something new for (scheduler.RunFeeds). The per-tracker
// rotation stays underneath as the slower sweep that catches whatever a feed
// missed.
//
// A feed row is a hint, never a reading: it triggers an ordinary resolve of
// the series, and what that resolve says is what gets stored. So a feed may
// be approximate — a number that counts every language, a listing that only
// reaches back a quarter of an hour — without ever putting a wrong chapter in
// front of the reader.
type UpdateFeed interface {
	// RecentUpdates returns the series the site updated most recently. The
	// order does not matter and a series may appear once at most.
	RecentUpdates(ctx context.Context) ([]SeriesUpdate, error)
	// FeedKey returns the key RecentUpdates' rows carry for the series a
	// linked URL names, and false for a URL that is not a series of the site.
	FeedKey(rawURL string) (string, bool)
}

// SeriesUpdate is one row of a site's update feed.
type SeriesUpdate struct {
	// Key identifies the series the way FeedKey derives it from a linked URL.
	Key string
	// Chapter is the newest chapter number the feed shows for the series.
	Chapter float64
	// UpdatedAt is when the site says that chapter landed, nil when the feed
	// does not date its rows.
	UpdatedAt *time.Time
}

// FeedRows collects feed rows into one per series, keeping the highest chapter
// a series was listed with. Feeds list uploads, not series: a second group's
// upload, or two chapters released together, put the same series in the
// listing more than once.
type FeedRows struct {
	byKey map[string]SeriesUpdate
}

// Add records one listing entry. Entries without a key or with a chapter
// number not worth acting on (ValidChapterWithin the given bound) are dropped.
func (rows *FeedRows) Add(key string, chapter float64, updatedAt *time.Time, maxChapter float64) {
	key = strings.TrimSpace(key)
	if key == "" || !ValidChapterWithin(chapter, maxChapter) {
		return
	}
	if rows.byKey == nil {
		rows.byKey = map[string]SeriesUpdate{}
	}
	if existing, ok := rows.byKey[key]; ok && existing.Chapter >= chapter {
		return
	}
	rows.byKey[key] = SeriesUpdate{Key: key, Chapter: chapter, UpdatedAt: updatedAt}
}

// Result returns the collected rows, sorted by key so a feed reads the same
// from one call to the next.
func (rows *FeedRows) Result() []SeriesUpdate {
	updates := make([]SeriesUpdate, 0, len(rows.byKey))
	for _, update := range rows.byKey {
		updates = append(updates, update)
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Key < updates[j].Key })
	return updates
}
