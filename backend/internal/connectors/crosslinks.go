package connectors

import (
	"context"
	"net/url"
	"strings"
)

// Cross links are a site's own pointers to the same work on other sites:
// MangaFire's title record names the series' MangaDex, MangaUpdates and
// MangaBaka pages, for instance. They turn linking from fuzzy title matching
// into an id lookup — the series MangaFire says it is, rather than whichever
// search hit shares its title — and they can tell a stored link that names a
// different series (a light novel linked where the manga was meant) from one
// that agrees.
//
// They name the work, not the edition: MangaFire's "(Official Colored)"
// reprint points at the black-and-white original's MangaUpdates entry. So a
// cross link says which series a page belongs to, never that two pages carry
// the same chapters, and whoever links on it still compares chapter numbers.

// Keys of the cross links that are not sources of their own. Links to sites
// that are sources are keyed by that source's connector key ("mangadex",
// "mangaupdates").
const (
	// CrossLinkMangaBaka is the MangaBaka series page, whose record merges the
	// series' entries on AniList, MyAnimeList, MangaUpdates and others.
	CrossLinkMangaBaka = "mangabaka"
)

// CrossLinkReader is implemented by connectors whose series record names the
// series' pages on other sites. CrossLinks returns them keyed by the other
// site's connector key (or a CrossLink* constant), each value a full URL; an
// empty map with no error means the site names none. Implementations answer
// from the record they already keep for resolves wherever they can, so asking
// about a series the poller visited recently costs no request.
type CrossLinkReader interface {
	CrossLinks(ctx context.Context, rawURL string) (map[string]string, error)
	// CrossLinkKeys lists the keys CrossLinks can answer with, so a caller
	// can tell without asking whether the reader could name a given site.
	CrossLinkKeys() []string
}

// SeriesKeyer is implemented by connectors that can name the series a URL of
// theirs points at, independent of how the URL is spelled: two URLs with the
// same key are the same series (MangaUpdates' "/series/0abc12/slug" and
// "/series/abc12" alike). A connector with an update feed already derives
// such a key (UpdateFeed.FeedKey) and need not implement this as well.
type SeriesKeyer interface {
	SeriesKey(rawURL string) (string, bool)
}

// SeriesKeyOf names the series rawURL points at on the connector's site:
// through SeriesKeyer or UpdateFeed when the connector implements one, and
// otherwise from the URL itself with the differences that never change the
// page (scheme, a "www." prefix, case, a trailing slash, the query) taken out.
// It returns "" for a URL that cannot be parsed or that the connector does not
// recognise as one of its series.
func SeriesKeyOf(connector Connector, rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if keyer, ok := connector.(SeriesKeyer); ok {
		if key, ok := keyer.SeriesKey(rawURL); ok {
			return key
		}
		return ""
	}
	if feed, ok := connector.(UpdateFeed); ok {
		if key, ok := feed.FeedKey(rawURL); ok {
			return key
		}
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	path := strings.TrimRight(strings.ToLower(parsed.EscapedPath()), "/")
	return host + path
}

// SameSeries reports whether two URLs on the connector's site name the same
// series. Two URLs it cannot name are never the same.
func SameSeries(connector Connector, a, b string) bool {
	keyA := SeriesKeyOf(connector, a)
	return keyA != "" && keyA == SeriesKeyOf(connector, b)
}
