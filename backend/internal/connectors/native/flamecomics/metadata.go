package flamecomics

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

var (
	nextDataPattern     = regexp.MustCompile(`(?is)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
	jsonLDScriptPattern = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
)

// nextDataPayload is the part of the page's __NEXT_DATA__ the metadata is read
// from: the series record the page was rendered from, the same object Flame's
// own API serves. It is decoded loosely (a map, not a struct) because the
// record's fields are not typed consistently enough to trust: author is a
// list, year a number, and either could change shape without notice.
type nextDataPayload struct {
	Props struct {
		PageProps struct {
			Series map[string]any `json:"series"`
		} `json:"pageProps"`
	} `json:"props"`
}

// extractMetadata reads what the series page says about the series besides
// its chapters, from the page the resolve already downloaded. The series
// record in __NEXT_DATA__ carries everything (type, country, status, year,
// tags, creators, an HTML synopsis); the JSON-LD series block fills in what
// the record lacks, and stands alone should the site move off the pages
// router and stop embedding __NEXT_DATA__. A page that says nothing yields nil,
// and nothing here can fail the resolve: a payload that does not parse is
// skipped.
func extractMetadata(body string) *connectors.SeriesMetadata {
	meta := &connectors.SeriesMetadata{}

	var payload nextDataPayload
	if raw := connectors.FirstSubmatch(nextDataPattern, body); raw != "" && json.Unmarshal([]byte(raw), &payload) == nil {
		if series := payload.Props.PageProps.Series; series != nil {
			meta.Type = connectors.NormalizeSeriesType(jsonScalar(series["type"]))
			if meta.Type == "" {
				meta.Type = connectors.SeriesTypeFromCountry(jsonScalar(series["country"]))
			}
			meta.Status = connectors.NormalizeSeriesStatus(jsonScalar(series["status"]))
			meta.Year = parseYear(jsonScalar(series["year"]))
			// Flame keeps one list, mixing genres with demographics
			// ("Shounen"); it is reported whole and the consumer sorts it.
			meta.Genres = connectors.SplitLabels(strings.Join(jsonStrings(series["tags"]), ","))
			meta.Authors = connectors.CleanLabels(jsonStrings(series["author"]))
			meta.Artists = connectors.CleanLabels(jsonStrings(series["artist"]))
			meta.Description = connectors.PlainDescription(jsonScalar(series["description"]))
		}
	}

	if work := findSeriesJSONLD(body); work != nil {
		if len(meta.Genres) == 0 {
			meta.Genres = connectors.SplitLabels(strings.Join(jsonStrings(work["genre"]), ","))
		}
		if len(meta.Authors) == 0 {
			meta.Authors = connectors.CleanLabels(jsonStrings(work["author"]))
		}
		if len(meta.Artists) == 0 {
			meta.Artists = connectors.CleanLabels(jsonStrings(work["illustrator"]))
		}
		if meta.Description == "" {
			meta.Description = connectors.PlainDescription(jsonScalar(work["description"]))
		}
		if meta.Year == 0 {
			meta.Year = parseYear(jsonScalar(work["datePublished"]))
		}
	}

	if meta.Empty() {
		return nil
	}
	return meta
}

// findSeriesJSONLD returns the JSON-LD node describing the series, nil when
// the page has none. Flame types it CreativeWorkSeries (verified 2026-11);
// ComicSeries is accepted as well, being the more specific type a redesign
// would likely switch to.
func findSeriesJSONLD(body string) map[string]any {
	for _, match := range jsonLDScriptPattern.FindAllStringSubmatch(body, -1) {
		for _, node := range jsonLDNodes(match[1]) {
			for _, typeName := range jsonStrings(node["@type"]) {
				switch strings.ToLower(strings.TrimSpace(typeName)) {
				case "creativeworkseries", "comicseries":
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

// jsonStrings flattens a decoded JSON value into the strings it names: a
// string is itself, a number its shortest form, an object (a JSON-LD Person)
// its name, and an array each of its items. It reads the series record's
// lists and the JSON-LD's Person-or-list fields alike.
func jsonStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}
	case map[string]any:
		return jsonStrings(typed["name"])
	case []any:
		var values []string
		for _, item := range typed {
			values = append(values, jsonStrings(item)...)
		}
		return values
	}
	return nil
}

// jsonScalar reads a value the site may send as a string or a number (2021 or
// "2021"), "" for anything else.
func jsonScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	}
	return ""
}

// parseYear reads the start year from a year ("2021") or a date that begins
// with one ("2021-01-01"), 0 when it is not a plausible publication year.
func parseYear(raw string) int {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4 {
		raw = raw[:4]
	}
	year, err := strconv.Atoi(raw)
	if err != nil || year < 1900 || year > time.Now().Year()+1 {
		return 0
	}
	return year
}
