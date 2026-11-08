package metadata

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// SourceEntry is one linked site's metadata about a tracker's series.
type SourceEntry struct {
	SourceKey  string
	SourceName string
	SourceURL  string
	Metadata   connectors.SeriesMetadata
}

// SourceRating is one site's community score, shown side by side in the info
// panel: sites rate on different crowds, so averaging them would invent a
// number nobody gave.
type SourceRating struct {
	SourceKey  string  `json:"sourceKey"`
	SourceName string  `json:"sourceName"`
	Rating     float64 `json:"rating"`
	Votes      int     `json:"votes,omitempty"`
}

// Consolidated is one tracker's metadata merged across its linked sites. It is
// stored as JSON (tracker_metadata.payload) and rendered by the info panel.
type Consolidated struct {
	Type              string         `json:"type,omitempty"`
	Status            string         `json:"status,omitempty"`
	Year              int            `json:"year,omitempty"`
	Genres            []string       `json:"genres,omitempty"`
	Tags              []string       `json:"tags,omitempty"`
	Demographic       string         `json:"demographic,omitempty"`
	ContentRating     string         `json:"contentRating,omitempty"`
	Description       string         `json:"description,omitempty"`
	DescriptionSource string         `json:"descriptionSource,omitempty"`
	Authors           []string       `json:"authors,omitempty"`
	Artists           []string       `json:"artists,omitempty"`
	Ratings           []SourceRating `json:"ratings,omitempty"`
	// Sources are the display names of the sites that contributed anything.
	Sources []string `json:"sources,omitempty"`
}

// Empty reports whether nothing at all was learned.
func (c Consolidated) Empty() bool {
	return c.Type == "" && c.Status == "" && c.Year == 0 && len(c.Genres) == 0 && len(c.Tags) == 0 &&
		c.Description == "" && len(c.Authors) == 0 && len(c.Artists) == 0 && len(c.Ratings) == 0
}

// sourceWeight ranks the sites by how much their metadata can be trusted. The
// curated databases come first: MangaUpdates (community-maintained types and
// genres), MangaFire and MangaDex (AniList/MAL-grade records), then ComicK
// (MangaDex-derived). The origin scanlators describe their own series well but
// with their own vocabulary; the aggregators last.
var sourceWeight = map[string]int{
	"mangaupdates": 6,
	"mangafire":    5,
	"mangadex":     5,
	"comick":       4,
	"asuracomic":   3,
	"flamecomics":  3,
	"freewebnovel": 3,
	"webtoons":     2,
	"mangahub":     2,
	"mgeko":        1,
}

func weightOf(sourceKey string) int {
	if weight, ok := sourceWeight[strings.ToLower(sourceKey)]; ok {
		return weight
	}
	return 1
}

// Field-by-field precedence, best first. A field takes the first site in its
// list that has a value; sites not listed come after, by weight.
var (
	statusOrder      = []string{"mangafire", "mangadex", "comick", "mangaupdates", "asuracomic", "flamecomics", "freewebnovel", "webtoons", "mangahub", "mgeko"}
	yearOrder        = []string{"mangaupdates", "mangadex", "mangafire", "comick", "flamecomics"}
	descriptionOrder = []string{"mangadex", "mangafire", "mangaupdates", "comick", "asuracomic", "flamecomics", "webtoons", "freewebnovel", "mangahub", "mgeko"}
	peopleOrder      = []string{"mangaupdates", "mangadex", "mangafire", "comick", "asuracomic", "flamecomics", "webtoons", "freewebnovel", "mangahub", "mgeko"}
	demographicOrder = []string{"mangafire", "mangadex", "comick", "mangaupdates", "flamecomics"}
	ratingOrder      = []string{"mangadex", "mangafire", "comick"}
)

// minDescriptionRunes is how long a synopsis must be to win on precedence
// alone: a one-line blurb from a better site loses to a real paragraph from a
// lesser one.
const minDescriptionRunes = 80

// maxTags bounds the tags kept: MangaUpdates alone can carry dozens of
// categories, and the panel shows a summary.
const maxTags = 12

// Consolidate merges the sites' metadata into one view. It is a pure function
// of its input, order included only as the final tie-break.
func Consolidate(entries []SourceEntry) Consolidated {
	var out Consolidated
	if len(entries) == 0 {
		return out
	}

	out.Type = voteType(entries)
	out.Status = firstString(entries, statusOrder, func(m connectors.SeriesMetadata) string { return m.Status })
	if year := firstInt(entries, yearOrder, func(m connectors.SeriesMetadata) int { return m.Year }); year > 0 {
		out.Year = year
	}
	out.ContentRating = firstString(entries, ratingOrder, func(m connectors.SeriesMetadata) string { return m.ContentRating })
	out.Description, out.DescriptionSource = pickDescription(entries)
	out.Authors = firstList(entries, peopleOrder, func(m connectors.SeriesMetadata) []string { return m.Authors })
	out.Artists = firstList(entries, peopleOrder, func(m connectors.SeriesMetadata) []string { return m.Artists })

	genres, tags, demographic := mergeLabels(entries)
	out.Genres = genres
	out.Tags = tags
	out.Demographic = firstString(entries, demographicOrder, func(m connectors.SeriesMetadata) string {
		if class, canonical := classifyLabel(m.Demographic); class == labelDemographic {
			return canonical
		}
		return strings.TrimSpace(m.Demographic)
	})
	if out.Demographic == "" {
		out.Demographic = demographic
	}

	for _, entry := range ordered(entries, nil) {
		if entry.Metadata.Rating == nil || *entry.Metadata.Rating <= 0 {
			continue
		}
		out.Ratings = append(out.Ratings, SourceRating{
			SourceKey:  entry.SourceKey,
			SourceName: entry.SourceName,
			Rating:     *entry.Metadata.Rating,
			Votes:      entry.Metadata.RatingVotes,
		})
	}

	seenSources := map[string]struct{}{}
	for _, entry := range ordered(entries, nil) {
		if entry.Metadata.Empty() {
			continue
		}
		name := entry.SourceName
		if name == "" {
			name = entry.SourceKey
		}
		if _, ok := seenSources[name]; ok {
			continue
		}
		seenSources[name] = struct{}{}
		out.Sources = append(out.Sources, name)
	}
	return out
}

// voteType settles the series type by weighted vote: one site with the wrong
// type (a novel entry linked beside the manhwa, a scanlator calling everything
// "manhwa") is outvoted by the others instead of winning on precedence. A tie
// goes to the side with the single most trusted voter.
func voteType(entries []SourceEntry) string {
	totals := map[string]int{}
	best := map[string]int{}
	for _, entry := range entries {
		seriesType := strings.TrimSpace(entry.Metadata.Type)
		if seriesType == "" {
			continue
		}
		weight := weightOf(entry.SourceKey)
		totals[seriesType] += weight
		if weight > best[seriesType] {
			best[seriesType] = weight
		}
	}
	winner := ""
	for seriesType, total := range totals {
		switch {
		case winner == "":
			winner = seriesType
		case total > totals[winner]:
			winner = seriesType
		case total == totals[winner] && best[seriesType] > best[winner]:
			winner = seriesType
		case total == totals[winner] && best[seriesType] == best[winner] && seriesType < winner:
			winner = seriesType
		}
	}
	return winner
}

// ordered returns the entries sorted by precedence: first the keys in order
// (in that order), then the rest by weight, input order breaking ties.
func ordered(entries []SourceEntry, order []string) []SourceEntry {
	rank := func(key string) int {
		lowered := strings.ToLower(key)
		for index, candidate := range order {
			if candidate == lowered {
				return index
			}
		}
		return len(order)
	}
	sorted := make([]SourceEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := rank(sorted[i].SourceKey), rank(sorted[j].SourceKey)
		if ri != rj {
			return ri < rj
		}
		return weightOf(sorted[i].SourceKey) > weightOf(sorted[j].SourceKey)
	})
	return sorted
}

func firstString(entries []SourceEntry, order []string, field func(connectors.SeriesMetadata) string) string {
	for _, entry := range ordered(entries, order) {
		if value := strings.TrimSpace(field(entry.Metadata)); value != "" {
			return value
		}
	}
	return ""
}

func firstInt(entries []SourceEntry, order []string, field func(connectors.SeriesMetadata) int) int {
	for _, entry := range ordered(entries, order) {
		if value := field(entry.Metadata); value > 0 {
			return value
		}
	}
	return 0
}

func firstList(entries []SourceEntry, order []string, field func(connectors.SeriesMetadata) []string) []string {
	for _, entry := range ordered(entries, order) {
		if values := connectors.CleanLabels(field(entry.Metadata)); len(values) > 0 {
			return values
		}
	}
	return nil
}

// pickDescription takes the first synopsis by precedence that is long enough
// to be a real one, falling back to the longest on offer.
func pickDescription(entries []SourceEntry) (string, string) {
	longest, longestSource := "", ""
	for _, entry := range ordered(entries, descriptionOrder) {
		text := strings.TrimSpace(entry.Metadata.Description)
		if text == "" {
			continue
		}
		name := entry.SourceName
		if name == "" {
			name = entry.SourceKey
		}
		if utf8.RuneCountInString(text) >= minDescriptionRunes {
			return text, name
		}
		if utf8.RuneCountInString(text) > utf8.RuneCountInString(longest) {
			longest, longestSource = text, name
		}
	}
	return longest, longestSource
}

// mergeLabels runs every site's genres and tags through the vocabulary. Genres
// are the union of the canonical names any site gave, most agreed-on first;
// tags the leftovers, de-duplicated, most mentioned first and capped. The
// demographic a site filed among its genres is returned as a fallback.
func mergeLabels(entries []SourceEntry) ([]string, []string, string) {
	type tally struct {
		label string
		count int
		first int
	}
	genreCounts := map[string]*tally{}
	tagCounts := map[string]*tally{}
	demographic := ""
	position := 0

	add := func(table map[string]*tally, key string, label string) {
		position++
		if existing, ok := table[key]; ok {
			existing.count++
			return
		}
		table[key] = &tally{label: label, count: 1, first: position}
	}

	for _, entry := range ordered(entries, nil) {
		// One vote per site per label, however many times the site repeats it
		// across its genre and tag lists.
		seenGenres := map[string]struct{}{}
		seenTags := map[string]struct{}{}
		labels := append(append([]string{}, entry.Metadata.Genres...), entry.Metadata.Tags...)
		for _, label := range labels {
			class, canonical := classifyLabel(label)
			switch class {
			case labelGenre:
				if _, ok := seenGenres[canonical]; ok {
					continue
				}
				seenGenres[canonical] = struct{}{}
				add(genreCounts, canonical, canonical)
			case labelDemographic:
				if demographic == "" {
					demographic = canonical
				}
			case labelTag:
				key := labelKey(canonical)
				if _, ok := seenTags[key]; ok {
					continue
				}
				seenTags[key] = struct{}{}
				add(tagCounts, key, canonical)
			}
		}
	}

	flatten := func(table map[string]*tally, limit int) []string {
		if len(table) == 0 {
			return nil
		}
		items := make([]*tally, 0, len(table))
		for _, item := range table {
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].count != items[j].count {
				return items[i].count > items[j].count
			}
			return items[i].first < items[j].first
		})
		if limit > 0 && len(items) > limit {
			items = items[:limit]
		}
		labels := make([]string, len(items))
		for index, item := range items {
			labels[index] = item.label
		}
		return labels
	}

	return flatten(genreCounts, 0), flatten(tagCounts, maxTags), demographic
}

func sortStrings(values []string) {
	sort.Strings(values)
}
