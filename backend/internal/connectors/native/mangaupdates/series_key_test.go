package mangaupdates

import (
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// TestSeriesKeyIgnoresSpelling pins what lets a MangaFire cross link be
// compared with a stored link: the same series under every spelling the two
// sites use — zero padding, a "www.", a slug or none, a trailing slash.
func TestSeriesKeyIgnoresSpelling(t *testing.T) {
	connector := NewConnector()
	same := []string{
		"https://www.mangaupdates.com/series/04gtb06/tensei-shitara-slime-datta-ken",
		"https://mangaupdates.com/series/4gtb06/",
		"https://www.mangaupdates.com/series/04GTB06",
	}
	for _, url := range same[1:] {
		if !connectors.SameSeries(connector, same[0], url) {
			t.Errorf("%q and %q should name the same series", same[0], url)
		}
	}

	// The light novel of the same title is another series.
	if connectors.SameSeries(connector, same[0], "https://www.mangaupdates.com/series/q5n1lxg/tensei-shitara-slime-datta-ken-novel") {
		t.Error("the novel's entry must not count as the manga's")
	}
	if _, ok := connector.SeriesKey("https://www.mangaupdates.com/releases"); ok {
		t.Error("a page that is not a series has no series key")
	}
	if _, ok := connector.SeriesKey("https://mangadex.org/title/04gtb06"); ok {
		t.Error("another site's URL has no MangaUpdates series key")
	}
}
