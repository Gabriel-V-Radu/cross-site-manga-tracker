package asuracomic

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// homeChapterLinkPattern matches the chapter links the home page's "latest
// updates" block carries: /comics/{slug}-{hash}/chapter/{n}. Unlike the series
// page patterns it insists on the /comics/ prefix, because the home page is
// the whole site's navigation and a looser match would pick up anything that
// ends in /chapter/N.
var homeChapterLinkPattern = regexp.MustCompile(`(?i)/comics/([a-z0-9-]+)/chapter/(\d+(?:\.\d+)?)`)

var _ connectors.UpdateFeed = (*Connector)(nil)

// RecentUpdates implements connectors.UpdateFeed from the home page, which
// lists the twenty or so series updated last with links to their newest
// chapters (verified 2026-11-09: one request, where asking the same of each
// series is a page of its own per tracker).
//
// Rows are keyed by the series slug without its hash suffix. The suffix is not
// stable — the home page carried -f212c582 on series whose stored links end in
// -19cdf401 or -7f873ca6 — while the slug before it is.
func (c *Connector) RecentUpdates(ctx context.Context) ([]connectors.SeriesUpdate, error) {
	body, err := c.fetchPage(ctx, c.AbsoluteURL("/"))
	if err != nil {
		return nil, fmt.Errorf("fetch asuracomic home page: %w", err)
	}

	var rows connectors.FeedRows
	for _, match := range homeChapterLinkPattern.FindAllStringSubmatch(body, -1) {
		chapter, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			continue
		}
		rows.Add(stripSeriesIDHashSuffix(match[1]), chapter, nil, connectors.MaxPlausibleChapter)
	}
	updates := rows.Result()
	// The same reasoning as HealthCheck: a home page that loads and lists no
	// chapter link at all is a redesign, not a quiet day.
	if len(updates) == 0 {
		return nil, fmt.Errorf("asuracomic home page: %w", connectors.ErrNoChaptersListed)
	}
	return updates, nil
}

// FeedKey implements connectors.UpdateFeed.
func (c *Connector) FeedKey(rawURL string) (string, bool) {
	seriesID, _, err := c.parseSeriesURL(rawURL)
	if err != nil {
		return "", false
	}
	key := stripSeriesIDHashSuffix(seriesID)
	return key, key != ""
}
