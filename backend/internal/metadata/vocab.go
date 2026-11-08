// Package metadata turns what each site says about a series
// (connectors.SeriesMetadata) into one view per tracker: a single type and
// status, a genre list in one vocabulary, the best synopsis on offer. It also
// stores what the poller hands it and keeps those per-tracker views current
// (Service).
package metadata

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Genres are the labels the dashboard lets a reader filter by. Every site
// spells them its own way (MangaUpdates' "Sci-fi" and "Shounen Ai", MangaDex's
// "Sci-Fi" and "Boys' Love", Asura's "Murim"), so each site label is mapped onto
// one of these through genreAliases; a label with no mapping is kept as a tag,
// shown in the info panel but not offered as a filter. Besides the classic
// genres the list promotes the handful of themes this library is actually
// browsed by — reincarnation, regression, villainess, games — because a filter
// with a few hundred MangaUpdates categories in it would be useless, and one
// without them would miss what manhwa readers look for.
var genreAliases = buildAliases(map[string][]string{
	"Action":           {"action"},
	"Adult":            {"adult", "smut", "hentai"},
	"Adventure":        {"adventure"},
	"Boys' Love":       {"boys love", "boy love", "shounen ai", "shonen ai", "yaoi", "bl"},
	"Comedy":           {"comedy"},
	"Cooking":          {"cooking", "food", "gourmet"},
	"Crime":            {"crime"},
	"Cultivation":      {"cultivation"},
	"Demons":           {"demons", "demon"},
	"Drama":            {"drama"},
	"Ecchi":            {"ecchi"},
	"Fantasy":          {"fantasy"},
	"Gender Bender":    {"gender bender", "genderswap", "gender swap"},
	"Girls' Love":      {"girls love", "girl love", "shoujo ai", "shojo ai", "yuri", "gl"},
	"Harem":            {"harem"},
	"Historical":       {"historical", "history"},
	"Horror":           {"horror"},
	"Isekai":           {"isekai"},
	"Magic":            {"magic"},
	"Magical Girls":    {"magical girls", "magical girl"},
	"Martial Arts":     {"martial arts", "martial art", "murim", "wuxia"},
	"Mature":           {"mature"},
	"Mecha":            {"mecha", "robots"},
	"Medical":          {"medical"},
	"Military":         {"military"},
	"Monsters":         {"monsters", "monster"},
	"Music":            {"music"},
	"Mystery":          {"mystery"},
	"Philosophical":    {"philosophical"},
	"Post-Apocalyptic": {"post apocalyptic", "post apocalypse", "apocalypse", "apocalyptic"},
	"Psychological":    {"psychological"},
	"Reincarnation":    {"reincarnation", "reincarnated", "transmigration"},
	"Revenge":          {"revenge"},
	"Reverse Harem":    {"reverse harem"},
	"Romance":          {"romance"},
	"School Life":      {"school life", "school"},
	"Sci-Fi":           {"sci fi", "scifi", "science fiction"},
	"Slice of Life":    {"slice of life"},
	"Sports":           {"sports", "sport"},
	"Superhero":        {"superhero", "superheroes", "super hero", "super heroes"},
	"Supernatural":     {"supernatural"},
	"Survival":         {"survival"},
	"Thriller":         {"thriller"},
	"Time Travel":      {"time travel", "regression", "regressor", "time rewind", "time loop"},
	"Tragedy":          {"tragedy"},
	"Video Games":      {"video games", "video game", "game", "games", "gaming", "game elements", "virtual reality", "vr", "vrmmo"},
	"Villainess":       {"villainess"},
})

// demographicAliases are the readership labels some sites file among the
// genres (MangaUpdates, Flame); they become the demographic instead.
var demographicAliases = buildAliases(map[string][]string{
	"Shounen": {"shounen", "shonen"},
	"Seinen":  {"seinen"},
	"Shoujo":  {"shoujo", "shojo"},
	"Josei":   {"josei"},
})

// ignoredLabels are format, rating and type words that sites mix into their
// genre lists (MangaHub's "Long Strip, Web Comic, Full Color, Safe"). They say
// nothing about the story and are dropped rather than shown as tags.
var ignoredLabels = map[string]struct{}{}

func init() {
	for _, label := range []string{
		"long strip", "web comic", "webcomic", "webtoon", "webtoons", "full color", "official colored",
		"fan colored", "adaptation", "oneshot", "one shot", "4 koma", "anthology", "award winning",
		"self published", "user created", "safe", "suggestive", "erotica", "pornographic",
		"manga", "manhwa", "manhua", "novel", "doujinshi", "korean", "chinese", "japanese", "comic",
		"original", "english",
	} {
		ignoredLabels[label] = struct{}{}
	}
}

func buildAliases(groups map[string][]string) map[string]string {
	aliases := make(map[string]string)
	for canonical, spellings := range groups {
		aliases[labelKey(canonical)] = canonical
		for _, spelling := range spellings {
			aliases[labelKey(spelling)] = canonical
		}
	}
	return aliases
}

// labelKey folds a label to what the alias tables are keyed by: lowercase,
// apostrophes dropped, dashes, underscores and dots as spaces, spaces
// collapsed ("Boys’ Love" and "boys-love" both become "boys love").
func labelKey(label string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(label) {
		switch r {
		case '\'', '’', '‘', '`':
			continue
		case '-', '_', '.', '/':
			builder.WriteRune(' ')
		default:
			builder.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// labelClass is what one site label turned out to be.
type labelClass int

const (
	labelTag labelClass = iota
	labelGenre
	labelDemographic
	labelIgnored
)

// classifyLabel maps one site label onto the vocabulary: a canonical genre, a
// demographic, a word to drop, or a tag kept under its own (tidied) name.
func classifyLabel(label string) (labelClass, string) {
	key := labelKey(label)
	if key == "" {
		return labelIgnored, ""
	}
	if canonical, ok := genreAliases[key]; ok {
		return labelGenre, canonical
	}
	if canonical, ok := demographicAliases[key]; ok {
		return labelDemographic, canonical
	}
	if _, ok := ignoredLabels[key]; ok {
		return labelIgnored, ""
	}
	return labelTag, tidyTag(label)
}

// tidyTag gives a tag a consistent face: Webtoons' "ROMANCE" and a lowercase
// slug-like label are title-cased, a label already in mixed case is kept as
// its site wrote it.
func tidyTag(label string) string {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return ""
	}
	if label != strings.ToUpper(label) && label != strings.ToLower(label) {
		return label
	}
	words := strings.Fields(strings.ToLower(label))
	for index, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[index] = string(unicode.ToUpper(first)) + word[size:]
	}
	return strings.Join(words, " ")
}

// CanonicalGenres lists every genre a tracker can be filed under, in display
// order. Used by tests and by the filter, which only offers the ones present
// in the library.
func CanonicalGenres() []string {
	seen := map[string]struct{}{}
	genres := make([]string, 0, len(genreAliases))
	for _, canonical := range genreAliases {
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		genres = append(genres, canonical)
	}
	sortStrings(genres)
	return genres
}

// TypeLabel is the display name of a connectors.SeriesType value.
func TypeLabel(seriesType string) string {
	switch seriesType {
	case "manga":
		return "Manga"
	case "manhwa":
		return "Manhwa"
	case "manhua":
		return "Manhua"
	case "novel":
		return "Novel"
	case "comic":
		return "Comic"
	case "other":
		return "Other"
	}
	return ""
}

// StatusLabel is the display name of a connectors.SeriesStatus value.
func StatusLabel(status string) string {
	switch status {
	case "ongoing":
		return "Ongoing"
	case "completed":
		return "Completed"
	case "hiatus":
		return "Hiatus"
	case "cancelled":
		return "Cancelled"
	}
	return ""
}

// SeriesTypes are the type filter's options in display order.
var SeriesTypes = []string{"manga", "manhwa", "manhua", "novel", "comic", "other"}

// SeriesStatuses are the publication filter's options in display order.
var SeriesStatuses = []string{"ongoing", "completed", "hiatus", "cancelled"}
