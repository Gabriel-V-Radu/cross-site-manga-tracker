package asuracomic

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

var (
	jsonLDScriptPattern = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	// The rendered page prints status and type in labelled boxes: a label div
	// ("Status", "Type") followed by a div holding a colour dot and the value.
	statusBoxPattern = regexp.MustCompile(`(?is)>\s*Status\s*</div>\s*<div[^>]*>(.*?)</div>`)
	typeBoxPattern   = regexp.MustCompile(`(?is)>\s*Type\s*</div>\s*<div[^>]*>(.*?)</div>`)
	// descriptionTextPattern is the synopsis block under the title. It is the
	// only place that carries the whole text: the JSON-LD and og:description
	// copies are cut at about 300 characters and end in "...".
	descriptionTextPattern = regexp.MustCompile(`(?is)<div[^>]+id=["']description-text["'][^>]*>(.*?)</div>`)
	metaDescriptionPattern = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:property=["']og:description["']|name=["']description["'])[^>]*content="([^"]*)"`)
	// browseFilterAnchorPattern matches the genre chips and the creator rows,
	// which link to the browse page filtered by that value. They stand in for
	// the JSON-LD lists when the page stops carrying that block.
	browseFilterAnchorPattern = regexp.MustCompile(`(?is)<a[^>]+href=["'][^"']*/browse\?(genres|author|artist)=[^"']*["'][^>]*>(.*?)</a>`)
)

// extractMetadata reads what the series page says about the series besides
// its chapters, from the page the resolve already downloaded. The JSON-LD
// ComicSeries block carries the genres, creators and community score; status
// and type are only in the rendered boxes; the synopsis is taken from wherever
// the page has it whole. A page that says nothing yields nil, and nothing here
// can fail the resolve: a block that does not parse is skipped.
func extractMetadata(body string) *connectors.SeriesMetadata {
	meta := &connectors.SeriesMetadata{
		Type:   connectors.NormalizeSeriesType(connectors.CleanText(connectors.FirstSubmatch(typeBoxPattern, body))),
		Status: normalizeStatus(connectors.CleanText(connectors.FirstSubmatch(statusBoxPattern, body))),
	}

	descriptions := []string{connectors.FirstSubmatch(descriptionTextPattern, body)}
	if series := findComicSeries(body); series != nil {
		// Genre is a list on the live page, but schema.org allows one
		// comma-separated string, so every entry is split as well.
		meta.Genres = connectors.SplitLabels(strings.Join(jsonLDStrings(series["genre"]), ","))
		meta.Authors = splitPeople(jsonLDStrings(series["author"]))
		meta.Artists = splitPeople(jsonLDStrings(series["illustrator"]))
		meta.Rating, meta.RatingVotes = jsonLDRating(series["aggregateRating"])
		descriptions = append(descriptions, jsonLDScalar(series["description"]))
	}
	for _, match := range metaDescriptionPattern.FindAllStringSubmatch(body, -1) {
		descriptions = append(descriptions, match[1])
	}
	meta.Description = pickDescription(descriptions)

	if len(meta.Genres) == 0 || len(meta.Authors) == 0 || len(meta.Artists) == 0 {
		var genres, authors, artists []string
		for _, match := range browseFilterAnchorPattern.FindAllStringSubmatch(body, -1) {
			text := connectors.CleanText(match[2])
			switch strings.ToLower(match[1]) {
			case "genres":
				genres = append(genres, text)
			case "author":
				authors = append(authors, text)
			case "artist":
				artists = append(artists, text)
			}
		}
		if len(meta.Genres) == 0 {
			meta.Genres = connectors.CleanLabels(genres)
		}
		if len(meta.Authors) == 0 {
			meta.Authors = splitPeople(authors)
		}
		if len(meta.Artists) == 0 {
			meta.Artists = splitPeople(artists)
		}
	}

	if meta.Empty() {
		return nil
	}
	return meta
}

// normalizeStatus maps the Status box onto the shared vocabulary. Asura has
// one wording of its own: "Season End" marks a series paused between seasons,
// which is a hiatus by another name.
func normalizeStatus(raw string) string {
	if strings.EqualFold(strings.Join(strings.Fields(raw), " "), "season end") {
		return connectors.SeriesStatusHiatus
	}
	return connectors.NormalizeSeriesStatus(raw)
}

// findComicSeries returns the first JSON-LD node typed ComicSeries, nil when
// the page has none. The page carries several blocks (Organization,
// BreadcrumbList, an Article with a subset of the same fields), and only the
// ComicSeries one has the illustrator and the rating.
func findComicSeries(body string) map[string]any {
	for _, match := range jsonLDScriptPattern.FindAllStringSubmatch(body, -1) {
		for _, node := range jsonLDNodes(match[1]) {
			for _, typeName := range jsonLDStrings(node["@type"]) {
				if strings.EqualFold(strings.TrimSpace(typeName), "ComicSeries") {
					return node
				}
			}
		}
	}
	return nil
}

// jsonLDNodes decodes one ld+json block into the objects it describes: a
// single object, an array of them, or an object whose @graph holds them. A
// block that is not valid JSON yields nothing.
func jsonLDNodes(raw string) []map[string]any {
	var decoded any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return nil
	}
	var nodes []map[string]any
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			nodes = append(nodes, typed)
			if graph, ok := typed["@graph"]; ok {
				walk(graph)
			}
		}
	}
	walk(decoded)
	return nodes
}

// jsonLDStrings flattens a JSON-LD value into the strings it names, however
// the site chose to shape it: a string is itself, a number its shortest
// form, an object (a Person) its name, and an array each of its items. That
// covers author as one Person or a list of them, and genre as a list or a
// single string.
func jsonLDStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}
	case map[string]any:
		return jsonLDStrings(typed["name"])
	case []any:
		var values []string
		for _, item := range typed {
			values = append(values, jsonLDStrings(item)...)
		}
		return values
	}
	return nil
}

// jsonLDScalar reads a value the site may send as a string or a number
// ("8.9" or 8.9), "" for anything else.
func jsonLDScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	}
	return ""
}

// jsonLDRating reads an AggregateRating onto the 0-10 scale, scaled from its
// bestRating (10 when it does not say). The vote count is only kept beside a
// score it belongs to.
func jsonLDRating(value any) (*float64, int) {
	rating, ok := value.(map[string]any)
	if !ok {
		return nil, 0
	}
	outOf := 10.0
	if best, err := strconv.ParseFloat(jsonLDScalar(rating["bestRating"]), 64); err == nil && best > 0 {
		outOf = best
	}
	score := connectors.ParseRating(jsonLDScalar(rating["ratingValue"]), outOf)
	if score == nil {
		return nil, 0
	}
	votes := parseCount(jsonLDScalar(rating["ratingCount"]))
	if votes == 0 {
		votes = parseCount(jsonLDScalar(rating["reviewCount"]))
	}
	return score, votes
}

// parseCount reads a vote count ("39178", "39,178"), 0 when it is not a
// positive number.
func parseCount(raw string) int {
	value, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(raw), ",", ""), 64)
	if err != nil || value <= 0 || value > 1e9 {
		return 0
	}
	return int(value)
}

// splitPeople cleans a list of creator names. Asura keeps each role in one
// free-text field and packs collaborators into it ("AZI,COBY /Emperor
// Penguin"), so commas and slashes separate names as well.
func splitPeople(values []string) []string {
	var names []string
	for _, value := range values {
		names = append(names, strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '/' || r == ';' || r == '|'
		})...)
	}
	return connectors.CleanLabels(names)
}

// pickDescription chooses the synopsis among the page's copies of it: the
// longest one that is whole, or the longest of the cut ones when none is. A
// copy counts as cut when it ends in an ellipsis, which is how the JSON-LD
// and meta copies are shortened. A synopsis that genuinely ends in "..."
// therefore loses that preference, but its shorter copies are cut as well, so
// it still wins as the longest.
func pickDescription(candidates []string) string {
	var whole, cut string
	for _, candidate := range candidates {
		text := connectors.PlainDescription(candidate)
		if text == "" {
			continue
		}
		if strings.HasSuffix(text, "...") || strings.HasSuffix(text, "…") {
			if len(text) > len(cut) {
				cut = text
			}
			continue
		}
		if len(text) > len(whole) {
			whole = text
		}
	}
	if whole != "" {
		return whole
	}
	return cut
}
