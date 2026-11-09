package comick

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// recentChapter is one row of the newest-chapters listing: an upload and the
// comic it belongs to.
type recentChapter struct {
	Chap      string `json:"chap"`
	PublishAt string `json:"publish_at"`
	CreatedAt string `json:"created_at"`
	Comic     struct {
		Slug string `json:"slug"`
	} `json:"md_comics"`
}

var _ connectors.UpdateFeed = (*Connector)(nil)

// RecentUpdates implements connectors.UpdateFeed from the site's
// newest-chapters listing: GET /chapter?lang=en&order=new&page=1, the request
// behind the home page's "latest" column.
//
// The listing is shallow. Measured 2026-11-09 a page is 24 uploads, about a
// quarter of an hour of the site's English output, the page size cannot be
// raised (limit is ignored) and a second page came back shorter than the
// first rather than continuing it. So this feed only catches a release when it
// is read every few minutes, and whatever it misses is left to the tracker's
// ordinary turn on this site; it is read anyway because ComicK is the site
// that carries a new chapter first most often.
func (c *Connector) RecentUpdates(ctx context.Context) ([]connectors.SeriesUpdate, error) {
	var listing []recentChapter
	if err := c.fetchJSON(ctx, c.apiURL+"/chapter?lang=en&page=1&order=new&accept_mature_content=true", &listing); err != nil {
		return nil, fmt.Errorf("fetch comick recent chapters: %w", err)
	}

	var rows connectors.FeedRows
	for _, entry := range listing {
		number, err := strconv.ParseFloat(strings.TrimSpace(entry.Chap), 64)
		if err != nil {
			continue
		}
		rows.Add(feedKeyOf(entry.Comic.Slug), number, parseChapterTime(entry.PublishAt, entry.CreatedAt), connectors.MaxPlausibleChapter)
	}
	return rows.Result(), nil
}

// FeedKey implements connectors.UpdateFeed: the comic's slug, which is what a
// series URL carries and what the listing names each upload's comic by.
func (c *Connector) FeedKey(rawURL string) (string, bool) {
	slug, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", false
	}
	return feedKeyOf(slug), true
}

func feedKeyOf(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}
