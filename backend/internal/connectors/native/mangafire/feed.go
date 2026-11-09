package mangafire

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// recentTitlesLimit is how many titles one read of the latest-updates listing
// asks for. Measured 2026-11-09, a hundred titles reached back about four
// hours (the home page's own thirty, about two).
const recentTitlesLimit = 100

var _ connectors.UpdateFeed = (*Connector)(nil)

// RecentUpdates implements connectors.UpdateFeed from the listing behind the
// home page's "Latest Updates": the same signed /api/titles endpoint search
// uses, ordered by chapter_updated_at.
//
// Two things make its rows hints and nothing more, which is all a feed row is
// asked to be. The chapter number is the title payload's latestChapter, the
// highest in any language (see chapterLanguage), so for a title whose raws run
// ahead it names a chapter the English reader cannot open, and it does not
// move when an English chapter lands behind it; such a title is left to its
// other sites' feeds and to its ordinary turn here. And the rows carry no
// timestamp, only "37m ago".
//
// The site's own request also filters by content_rating[] and genres_ex[],
// sent as repeated keys; the signer signs one value per key, and the listing
// answers without them.
func (c *Connector) RecentUpdates(ctx context.Context) ([]connectors.SeriesUpdate, error) {
	params := url.Values{}
	params.Set("order[chapter_updated_at]", "desc")
	params.Set("page", "1")
	params.Set("limit", strconv.Itoa(recentTitlesLimit))

	var response apiTitlesResponse
	if err := c.fetchAPI(ctx, "/api/titles", params, &response); err != nil {
		return nil, fmt.Errorf("fetch mangafire latest updates: %w", err)
	}

	var rows connectors.FeedRows
	for _, item := range response.Items {
		if item.LatestChapter == nil {
			continue
		}
		rows.Add(feedKeyOf(item.HID), *item.LatestChapter, nil, connectors.MaxPlausibleChapter)
	}
	return rows.Result(), nil
}

// FeedKey implements connectors.UpdateFeed: the title's hid, which every URL
// form a tracker may have stored carries (see parseTitleURL).
func (c *Connector) FeedKey(rawURL string) (string, bool) {
	hid, _, err := c.parseTitleURL(rawURL)
	if err != nil {
		return "", false
	}
	return feedKeyOf(hid), true
}

func feedKeyOf(hid string) string {
	return strings.ToLower(strings.TrimSpace(hid))
}
