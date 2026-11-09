package mangadex

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// recentChaptersLimit is the API's per-page ceiling for /chapter. Measured
// 2026-11-09, a hundred English uploads reached back more than eight hours, so
// one page read every few minutes misses nothing.
const recentChaptersLimit = 100

var _ connectors.UpdateFeed = (*Connector)(nil)

// RecentUpdates implements connectors.UpdateFeed from the site-wide chapter
// listing, newest first: GET /chapter?translatedLanguage[]=en&order[readableAt]=desc.
// Each entry names its title through a "manga" relationship, so one request
// covers every title on the site, where the per-title feed is a request per
// tracker.
//
// The filters are the per-title feed's (see feedURL): English only, every
// content rating, external chapters included. Chapters scheduled for later are
// left out, because they are not readable yet and a resolve would not find
// them.
func (c *Connector) RecentUpdates(ctx context.Context) ([]connectors.SeriesUpdate, error) {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(recentChaptersLimit))
	values.Set("order[readableAt]", "desc")
	values.Set("includeFutureUpdates", "0")
	values.Add("translatedLanguage[]", "en")
	values.Add("contentRating[]", "safe")
	values.Add("contentRating[]", "suggestive")
	values.Add("contentRating[]", "erotica")
	values.Add("contentRating[]", "pornographic")

	var payload recentChaptersResponse
	if err := connectors.FetchJSON(ctx, c.httpClient, c.apiBaseURL+"/chapter?"+values.Encode(), &payload); err != nil {
		return nil, fmt.Errorf("fetch mangadex recent chapters: %w", err)
	}

	var rows connectors.FeedRows
	for _, chapter := range payload.Data {
		number := parseChapterNumber(chapter.Attributes.Chapter)
		if number == nil {
			continue
		}
		for _, relationship := range chapter.Relationships {
			if relationship.Type != "manga" {
				continue
			}
			rows.Add(feedKeyOf(relationship.ID), *number,
				parseChapterTime(chapter.Attributes.PublishAt, chapter.Attributes.ReadableAt, chapter.Attributes.CreatedAt),
				connectors.MaxPlausibleChapter)
		}
	}
	return rows.Result(), nil
}

// FeedKey implements connectors.UpdateFeed: the title id.
func (c *Connector) FeedKey(rawURL string) (string, bool) {
	titleID, err := c.parseTitleURL(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}
	return feedKeyOf(titleID), true
}

// feedKeyOf spells a title id the one way both sides of the match use: stored
// links carry whatever casing they were pasted with.
func feedKeyOf(titleID string) string {
	return strings.ToLower(strings.TrimSpace(titleID))
}

type recentChaptersResponse struct {
	Data []struct {
		Attributes struct {
			Chapter    string `json:"chapter"`
			PublishAt  string `json:"publishAt"`
			ReadableAt string `json:"readableAt"`
			CreatedAt  string `json:"createdAt"`
		} `json:"attributes"`
		Relationships []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"relationships"`
	} `json:"data"`
}
