package connectors

import (
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// SeriesMetadata is what a site says about a series beyond its chapters: the
// kind of work it is, its genres, who made it and what it is about. A connector
// fills it from the payload its resolve already downloaded — the MangaFire title
// record, the MangaDex manga object, an Asura series page — so collecting it
// costs no extra request. Every field is optional; a site that does not say
// leaves it zero.
//
// Values are reported as the site gives them except where a fixed vocabulary
// exists: Type, Status and ContentRating go through the Normalize helpers below
// so a consumer can compare them across sites. Genres and Tags are kept as the
// site's own labels; mapping them onto one vocabulary is the consumer's job
// (internal/metadata), because which labels count as genres is a product choice,
// not a property of the site.
type SeriesMetadata struct {
	// Type is one of the SeriesType constants, or "" when the site does not say.
	Type string `json:"type,omitempty"`
	// Status is one of the SeriesStatus constants, or "".
	Status string `json:"status,omitempty"`
	// Year is the year the series started, 0 when unknown.
	Year int `json:"year,omitempty"`
	// Genres are the site's genre labels; Tags its themes or categories, when it
	// keeps them apart. A site with one undifferentiated list puts it in Genres.
	Genres []string `json:"genres,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	// Demographic is the target readership (Shounen, Seinen, Shoujo, Josei).
	Demographic string `json:"demographic,omitempty"`
	// ContentRating is one of the ContentRating constants, or "".
	ContentRating string `json:"contentRating,omitempty"`
	// Description is plain text (see PlainDescription), paragraphs separated by
	// a blank line.
	Description string   `json:"description,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Artists     []string `json:"artists,omitempty"`
	// Rating is the site's community score on a 0-10 scale, nil when it has
	// none; RatingVotes how many votes it averages, 0 when unknown.
	Rating      *float64 `json:"rating,omitempty"`
	RatingVotes int      `json:"ratingVotes,omitempty"`
}

// Empty reports whether the metadata carries nothing worth storing.
func (m *SeriesMetadata) Empty() bool {
	if m == nil {
		return true
	}
	return m.Type == "" && m.Status == "" && m.Year == 0 && len(m.Genres) == 0 && len(m.Tags) == 0 &&
		m.Demographic == "" && m.ContentRating == "" && m.Description == "" && len(m.Authors) == 0 &&
		len(m.Artists) == 0 && m.Rating == nil
}

// The series types every site's own wording is mapped onto.
const (
	SeriesTypeManga  = "manga"
	SeriesTypeManhwa = "manhwa"
	SeriesTypeManhua = "manhua"
	SeriesTypeNovel  = "novel"
	// SeriesTypeComic is a Western or English-original comic (OEL, webcomics
	// made in English).
	SeriesTypeComic = "comic"
	// SeriesTypeOther covers doujinshi, artbooks and anything else.
	SeriesTypeOther = "other"
)

// The publication states every site's wording is mapped onto.
const (
	SeriesStatusOngoing   = "ongoing"
	SeriesStatusCompleted = "completed"
	SeriesStatusHiatus    = "hiatus"
	SeriesStatusCancelled = "cancelled"
)

// The content ratings, MangaDex's scale, which MangaFire and ComicK share.
const (
	ContentRatingSafe         = "safe"
	ContentRatingSuggestive   = "suggestive"
	ContentRatingErotica      = "erotica"
	ContentRatingPornographic = "pornographic"
)

// NormalizeSeriesType maps a site's type label ("Manhwa", "MANHUA",
// "light_novel", "OEL", "Webtoon"…) onto a SeriesType constant, "" when the
// label is not a type this app knows. Webtoon is deliberately not mapped: it is
// a format (vertical scroll) that Korean, Chinese and Western works all use.
func NormalizeSeriesType(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.NewReplacer("_", " ", "-", " ").Replace(key)
	key = strings.Join(strings.Fields(key), " ")
	switch key {
	case "manga", "one shot", "oneshot", "japanese":
		return SeriesTypeManga
	case "manhwa", "korean":
		return SeriesTypeManhwa
	case "manhua", "chinese":
		return SeriesTypeManhua
	case "novel", "light novel", "web novel", "webnovel", "english novel", "ln", "wn":
		return SeriesTypeNovel
	case "oel", "comic", "comics", "western", "english":
		return SeriesTypeComic
	case "doujinshi", "doujin", "artbook", "art book", "other":
		return SeriesTypeOther
	}
	return ""
}

// SeriesTypeFromLanguage infers the type from the original language code
// (MangaDex's originalLanguage: "ja", "ko", "zh", "zh-hk", "en"…).
func SeriesTypeFromLanguage(code string) string {
	key := strings.ToLower(strings.TrimSpace(code))
	switch {
	case key == "ja" || key == "ja-ro":
		return SeriesTypeManga
	case key == "ko" || key == "ko-ro":
		return SeriesTypeManhwa
	case key == "zh" || strings.HasPrefix(key, "zh-"):
		return SeriesTypeManhua
	case key == "en":
		return SeriesTypeComic
	}
	return ""
}

// SeriesTypeFromCountry infers the type from a country code (ComicK's
// "jp"/"kr"/"cn", Flame's "KR").
func SeriesTypeFromCountry(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "jp":
		return SeriesTypeManga
	case "kr":
		return SeriesTypeManhwa
	case "cn", "hk", "tw":
		return SeriesTypeManhua
	case "us", "gb", "uk", "ca", "au":
		return SeriesTypeComic
	}
	return ""
}

// NormalizeSeriesStatus maps a site's status wording ("releasing", "OnGoing",
// "Finished", "on_hiatus", "discontinued"…) onto a SeriesStatus constant, ""
// when it is not one this app knows. Free text such as MangaUpdates' "135
// Chapters (Ongoing)" is matched by the word it contains.
func NormalizeSeriesStatus(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.NewReplacer("_", " ", "-", " ").Replace(key)
	switch {
	case key == "":
		return ""
	case strings.Contains(key, "hiatus"), key == "on hold", key == "paused":
		return SeriesStatusHiatus
	case strings.Contains(key, "cancel"), strings.Contains(key, "discontinued"), strings.Contains(key, "dropped"), strings.Contains(key, "axed"):
		return SeriesStatusCancelled
	case strings.Contains(key, "complete"), strings.Contains(key, "finished"), strings.Contains(key, "ended"):
		return SeriesStatusCompleted
	case strings.Contains(key, "ongoing"), strings.Contains(key, "releasing"), strings.Contains(key, "publishing"), strings.Contains(key, "airing"), key == "active":
		return SeriesStatusOngoing
	}
	return ""
}

// NormalizeContentRating maps a rating label onto a ContentRating constant.
func NormalizeContentRating(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "safe":
		return ContentRatingSafe
	case "suggestive":
		return ContentRatingSuggestive
	case "erotica":
		return ContentRatingErotica
	case "pornographic":
		return ContentRatingPornographic
	}
	return ""
}

var (
	// A break swallows the newline that usually follows it in the source, so
	// "<br>\n" is one line break, not a paragraph.
	descriptionBreakPattern     = regexp.MustCompile(`(?i)(?:<\s*br\s*/?\s*>|<\s*/\s*p\s*>|<\s*p[^>]*>)[ \t]*\n?`)
	descriptionBBCodePattern    = regexp.MustCompile(`(?i)\[/?(?:b|i|u|s|spoiler|center|quote|size[^\]]*|color[^\]]*|url[^\]]*|img|list|\*)\]`)
	descriptionMarkdownLink     = regexp.MustCompile(`\[([^\]]*)\]\((?:[^()]|\([^)]*\))*\)`)
	descriptionMarkdownEmphasis = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
	descriptionRulePattern      = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	descriptionInlineRule       = regexp.MustCompile(`-{3,}`)
	descriptionBlankLines       = regexp.MustCompile(`\n{3,}`)
	descriptionSpaces           = regexp.MustCompile(`[ \t\f\v\r]+`)
)

// maxDescriptionRunes bounds a stored description. Synopses run a few hundred
// words; anything longer is a site pasting notes, and the panel that shows it
// is a summary, not an archive.
const maxDescriptionRunes = 2400

// PlainDescription reduces a site's synopsis — HTML (MangaFire, Flame), BBCode
// (MangaUpdates) or Markdown (MangaDex, ComicK) — to plain text with blank
// lines between paragraphs. MangaDex's convention of appending links and notes
// after a horizontal rule is honoured by cutting there: what follows is a list
// of official sites, not the story.
func PlainDescription(raw string) string {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	text = descriptionBreakPattern.ReplaceAllString(text, "\n")
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	if location := descriptionRulePattern.FindStringIndex(text); location != nil && location[0] > 0 {
		text = text[:location[0]]
	}
	// ComicK flattens the rule into the text ("…bargained for….---Alternative
	// Official English: …"); a dash run that long never belongs to a sentence.
	if location := descriptionInlineRule.FindStringIndex(text); location != nil && location[0] > 40 {
		text = text[:location[0]]
	}
	text = descriptionMarkdownLink.ReplaceAllString(text, "$1")
	text = descriptionBBCodePattern.ReplaceAllString(text, "")
	text = descriptionMarkdownEmphasis.ReplaceAllString(text, "$2")
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSpace(descriptionSpaces.ReplaceAllString(line, " "))
	}
	text = strings.Join(lines, "\n")
	text = descriptionBlankLines.ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > maxDescriptionRunes {
		text = strings.TrimSpace(string(runes[:maxDescriptionRunes])) + "…"
	}
	return text
}

// CleanLabels trims, unescapes and de-duplicates a list of labels (genres,
// tags, people), case-insensitively and in order, dropping empties and the
// placeholders scrapers find where a site has nothing ("updating", "N/A").
func CleanLabels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		label := strings.Join(strings.Fields(html.UnescapeString(value)), " ")
		key := strings.ToLower(label)
		switch key {
		case "", "updating", "n/a", "na", "unknown", "none", "-":
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, label)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

// SplitLabels splits a delimited label list ("Action, Martial Arts; Drama")
// and cleans it.
func SplitLabels(raw string) []string {
	return CleanLabels(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|'
	}))
}

// ParseRating reads a community score that a site may send as a number or a
// string ("8.45"), scaled from outOf to 0-10. It returns nil for anything that
// is not a positive score within range.
func ParseRating(raw string, outOf float64) *float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	// NaN fails every comparison, so it would slip past the range checks
	// below; it is refused by name.
	if err != nil || math.IsNaN(value) || outOf <= 0 || value <= 0 || value > outOf {
		return nil
	}
	scaled := value * 10 / outOf
	scaled = float64(int(scaled*100+0.5)) / 100
	return &scaled
}
