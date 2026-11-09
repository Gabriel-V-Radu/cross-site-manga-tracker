package mangahub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// latestUpdatesLimit is the most rows the listing gives in one answer: asked
// for a hundred it returned fifty, which reached back a little over two hours
// (the default page of thirty, under one and a half). Measured 2026-11-09.
const latestUpdatesLimit = 50

// latestUpdatesQuery is the listing behind the site's "latest updates" page:
// the search query with no text, ordered by update (verified live 2026-11-09).
// It asks for the three fields a feed row needs and nothing else, so the
// answer stays a few kilobytes.
var latestUpdatesQuery = fmt.Sprintf("{search(x:%s,mod:LATEST,count:true,offset:0,limit:%d){rows{slug latestChapter updatedDate}}}", variantCode, latestUpdatesLimit)

var _ connectors.UpdateFeed = (*Connector)(nil)

// RecentUpdates implements connectors.UpdateFeed from the latest-updates
// listing, one POST.
func (c *Connector) RecentUpdates(ctx context.Context) ([]connectors.SeriesUpdate, error) {
	response, err := c.queryGraphQL(ctx, latestUpdatesQuery)
	if err != nil {
		return nil, fmt.Errorf("fetch mangahub latest updates: %w", err)
	}
	if response.Data.Search == nil {
		return nil, fmt.Errorf("mangahub latest updates returned no payload")
	}

	var rows connectors.FeedRows
	for _, manga := range response.Data.Search.Rows {
		if manga.LatestChapter == nil {
			continue
		}
		rows.Add(feedKeyOf(manga.Slug), *manga.LatestChapter,
			connectors.ParseFirstTime(manga.UpdatedDate, time.RFC3339), connectors.MaxPlausibleChapter)
	}
	return rows.Result(), nil
}

// FeedKey implements connectors.UpdateFeed: the series slug.
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
