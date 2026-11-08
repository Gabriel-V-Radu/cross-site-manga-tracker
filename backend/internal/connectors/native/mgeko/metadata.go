package mgeko

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

var (
	// The genre list is a <ul> of links under the header's categories block.
	categoriesBlockPattern = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*\bcategories\b[^"']*["'][^>]*>(.*?)</ul>`)
	anchorTextPattern      = regexp.MustCompile(`(?is)<a\b[^>]*>(.*?)</a>`)
	// The status is one of the header stats: <strong class="ongoing">Ongoing
	// </strong> <small>Status</small>, the class following the value.
	statusStatPattern = regexp.MustCompile(`(?is)<strong[^>]*>([^<]*)</strong>\s*<small[^>]*>\s*Status\s*</small>`)
	authorPattern     = regexp.MustCompile(`(?is)<span[^>]+itemprop=["']author["'][^>]*>(.*?)</span>`)
	// The tags (MangaUpdates-style themes) are pills under a Tags heading, kept
	// apart from the genres.
	tagPillPattern = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*\bmmtag-item\b[^"']*["'][^>]*>\s*<span[^>]*>(.*?)</span>`)
	// descriptionBlockPattern is the About tab's text. It opens with the
	// site's generated blurb and only then, after "The Summary is", gives the
	// real synopsis.
	descriptionBlockPattern = regexp.MustCompile(`(?is)<p[^>]+class=["'][^"']*\bdescription\b[^"']*["'][^>]*>(.*?)</p>`)
	summaryMarkerPattern    = regexp.MustCompile(`(?i)The\s+Summary\s+is\s*:?`)
	// The community score is schema.org microdata on the rating widget, on a
	// five-star scale.
	ratingValuePattern = regexp.MustCompile(`(?is)<meta[^>]+itemprop=["']ratingValue["'][^>]*content=["']([^"']*)["']`)
	ratingBestPattern  = regexp.MustCompile(`(?is)<meta[^>]+itemprop=["']bestRating["'][^>]*content=["']([^"']*)["']`)
	ratingCountPattern = regexp.MustCompile(`(?is)<meta[^>]+itemprop=["']ratingCount["'][^>]*content=["']([^"']*)["']`)
)

// extractMetadata reads what the manga page says about the series besides its
// chapters, from the page the resolve already downloaded: genres, tags,
// status, author, synopsis and the community score. A page that says nothing
// yields nil, and nothing here can fail the resolve.
//
// The meta and og descriptions are never used: they are one template for
// every series ("… is a manga/manhwa/manhua written by updating and is
// updated fast and free on www.mgeko.cc"), not a synopsis.
func extractMetadata(body string) *connectors.SeriesMetadata {
	meta := &connectors.SeriesMetadata{
		Status:      connectors.NormalizeSeriesStatus(connectors.CleanText(connectors.FirstSubmatch(statusStatPattern, body))),
		Description: extractSynopsis(body),
	}

	var genres []string
	for _, match := range anchorTextPattern.FindAllStringSubmatch(connectors.FirstSubmatch(categoriesBlockPattern, body), -1) {
		genres = append(genres, connectors.CleanText(match[1]))
	}
	meta.Genres = connectors.CleanLabels(genres)
	meta.Type = typeFromGenres(meta.Genres)

	var tags []string
	for _, match := range tagPillPattern.FindAllStringSubmatch(body, -1) {
		tags = append(tags, connectors.CleanText(match[1]))
	}
	meta.Tags = connectors.CleanLabels(tags)

	// The author span reads "updating" when the site has no name, which
	// CleanLabels drops as a placeholder.
	var authors []string
	for _, match := range authorPattern.FindAllStringSubmatch(body, -1) {
		authors = append(authors, connectors.CleanText(match[1]))
	}
	meta.Authors = connectors.SplitLabels(strings.Join(authors, ","))

	outOf := 5.0
	if best, err := strconv.ParseFloat(strings.TrimSpace(connectors.FirstSubmatch(ratingBestPattern, body)), 64); err == nil && best > 0 {
		outOf = best
	}
	// A series nobody has rated shows 0, which ParseRating already refuses.
	if meta.Rating = connectors.ParseRating(connectors.FirstSubmatch(ratingValuePattern, body), outOf); meta.Rating != nil {
		if votes, err := strconv.Atoi(strings.TrimSpace(connectors.FirstSubmatch(ratingCountPattern, body))); err == nil && votes > 0 {
			meta.RatingVotes = votes
		}
	}

	if meta.Empty() {
		return nil
	}
	return meta
}

// extractSynopsis returns the real synopsis from the About tab, "" when the
// page only has the generated blurb. The blurb ("… is a Manga/Manhwa/Manhua in
// english language, Comedy series … on www.mgeko.cc fastest updating comic
// site. The Summary is") is cut at its closing words; a block without them is
// kept only if it does not read like that blurb.
func extractSynopsis(body string) string {
	block := connectors.FirstSubmatch(descriptionBlockPattern, body)
	if block == "" {
		return ""
	}
	if location := summaryMarkerPattern.FindStringIndex(block); location != nil {
		return connectors.PlainDescription(block[location[1]:])
	}
	lowered := strings.ToLower(block)
	if strings.Contains(lowered, "mgeko") || strings.Contains(lowered, "manga/manhwa/manhua") {
		return ""
	}
	return connectors.PlainDescription(block)
}

// typeFromGenres reads the series type from the genre list, where Mgeko puts
// it for some series ("Manhwa" beside "Action"). The page has no type field of
// its own, so only an exact manga/manhwa/manhua label counts, and only when it
// is the only one: a label such as "One shot", which NormalizeSeriesType would
// read as manga, is a format the site applies to Korean works as well.
func typeFromGenres(genres []string) string {
	found := ""
	for _, genre := range genres {
		switch strings.ToLower(genre) {
		case "manga", "manhwa", "manhua":
			seriesType := connectors.NormalizeSeriesType(genre)
			if found != "" && found != seriesType {
				return ""
			}
			found = seriesType
		}
	}
	return found
}
