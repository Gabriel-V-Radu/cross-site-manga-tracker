package comick

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// apiComicDetail is the comic record as /comic/{slug} returns it: the fields a
// search row shares (apiComic) plus what the site says about the series. The
// search endpoint returns a slimmer row with some of the same fields in other
// shapes (genres as bare ids), so metadata is only ever read from this one.
//
// Every metadata field is decoded leniently (see looseString and friends): the
// comic record is what the resolve rests on — it supplies the hid the chapter
// request needs — so a field that changes shape must cost that field alone,
// never the whole resolve.
type apiComicDetail struct {
	apiComic

	Country        looseString `json:"country"`
	Status         looseNumber `json:"status"`
	Year           looseNumber `json:"year"`
	Demographic    looseNumber `json:"demographic"`
	ContentRating  looseString `json:"content_rating"`
	BayesianRating looseNumber `json:"bayesian_rating"`
	RatingCount    looseNumber `json:"rating_count"`
	Desc           looseString `json:"desc"`
	Genres         looseGenres `json:"md_comic_md_genres"`
}

// comicStatuses is ComicK's numeric publication status. 2 is the original
// run's end, regardless of whether the translation has caught up.
var comicStatuses = map[int]string{
	1: connectors.SeriesStatusOngoing,
	2: connectors.SeriesStatusCompleted,
	3: connectors.SeriesStatusCancelled,
	4: connectors.SeriesStatusHiatus,
}

// comicDemographics is the record's numeric demographic, the fallback for when
// the response's top-level label is missing.
var comicDemographics = map[int]string{
	1: "Shounen",
	2: "Shoujo",
	3: "Seinen",
	4: "Josei",
}

// metadata maps the /comic/{slug} response onto the shared vocabulary, nil
// when it says nothing worth storing. The people and the demographic label sit
// beside the comic object rather than inside it. ComicK files its tags in
// MangaDex's groups; genres and themes are kept, format tags ("Long Strip") and
// content warnings ("Gore") are not. bayesian_rating is the vote-weighted score
// the site shows, already on a 0-10 scale.
func (response apiComicResponse) metadata() *connectors.SeriesMetadata {
	comic := response.Comic

	var genres, tags []string
	for _, genre := range comic.Genres {
		name := string(genre.MDGenres.Name)
		switch strings.ToLower(strings.TrimSpace(string(genre.MDGenres.Group))) {
		case "genre":
			genres = append(genres, name)
		case "theme":
			tags = append(tags, name)
		}
	}

	metadata := &connectors.SeriesMetadata{
		Type:          connectors.SeriesTypeFromCountry(string(comic.Country)),
		Year:          comic.Year.year(),
		Genres:        connectors.CleanLabels(genres),
		Tags:          connectors.CleanLabels(tags),
		ContentRating: connectors.NormalizeContentRating(string(comic.ContentRating)),
		Description:   connectors.PlainDescription(string(comic.Desc)),
		Authors:       connectors.CleanLabels(response.Authors),
		Artists:       connectors.CleanLabels(response.Artists),
		Rating:        comic.BayesianRating.rating(10),
	}
	if code, ok := comic.Status.whole(); ok {
		metadata.Status = comicStatuses[code]
	}
	if labels := connectors.CleanLabels([]string{string(response.Demographic)}); len(labels) > 0 {
		metadata.Demographic = labels[0]
	} else if code, ok := comic.Demographic.whole(); ok {
		metadata.Demographic = comicDemographics[code]
	}
	if metadata.Rating != nil {
		metadata.RatingVotes = comic.RatingCount.count()
	}
	if metadata.Empty() {
		return nil
	}
	return metadata
}

// cloneMetadata copies a cached record's metadata for a result, so a caller
// that edits the result cannot reach into the cache every later resolve of
// the slug reads from.
func cloneMetadata(metadata *connectors.SeriesMetadata) *connectors.SeriesMetadata {
	if metadata == nil {
		return nil
	}
	clone := *metadata
	clone.Genres = cloneStrings(metadata.Genres)
	clone.Tags = cloneStrings(metadata.Tags)
	clone.Authors = cloneStrings(metadata.Authors)
	clone.Artists = cloneStrings(metadata.Artists)
	if metadata.Rating != nil {
		rating := *metadata.Rating
		clone.Rating = &rating
	}
	return &clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

// comicGenre is one entry of md_comic_md_genres. group is "Genre", "Theme",
// "Format" or "Content".
type comicGenre struct {
	MDGenres struct {
		Name  looseString `json:"name"`
		Group looseString `json:"group"`
	} `json:"md_genres"`
}

// looseGenres decodes md_comic_md_genres entry by entry, skipping any entry
// that is not an object, so one malformed tag cannot take the others with it.
type looseGenres []comicGenre

func (genres *looseGenres) UnmarshalJSON(data []byte) error {
	*genres = nil
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	for _, entry := range entries {
		var genre comicGenre
		if json.Unmarshal(entry, &genre) == nil {
			*genres = append(*genres, genre)
		}
	}
	return nil
}

// looseLabels decodes ComicK's [{"name":"…","slug":"…"}] people lists into
// their names. A plain string entry is taken as is, an entry of any other shape
// is skipped, and a value that is not a list at all decodes as empty.
type looseLabels []string

func (labels *looseLabels) UnmarshalJSON(data []byte) error {
	*labels = nil
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	for _, entry := range entries {
		var text string
		if json.Unmarshal(entry, &text) == nil {
			*labels = append(*labels, text)
			continue
		}
		var named struct {
			Name  looseString `json:"name"`
			Title looseString `json:"title"`
		}
		if json.Unmarshal(entry, &named) != nil {
			continue
		}
		if label := strings.TrimSpace(string(named.Name)); label != "" {
			*labels = append(*labels, label)
		} else if label := strings.TrimSpace(string(named.Title)); label != "" {
			*labels = append(*labels, label)
		}
	}
	return nil
}

// looseString is a JSON string that decodes anything else (null, a number, an
// object) as empty instead of failing.
type looseString string

func (s *looseString) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) != nil {
		value = ""
	}
	*s = looseString(value)
	return nil
}

// looseNumber is a JSON number the API may also send as a string —
// bayesian_rating arrives as "8.54" — or null. Anything that does not read as a
// finite number leaves it unset rather than failing the decode.
type looseNumber struct {
	value float64
	set   bool
}

func (n *looseNumber) UnmarshalJSON(data []byte) error {
	*n = looseNumber{}
	text := strings.TrimSpace(string(data))
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = strings.TrimSpace(unquoted)
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	*n = looseNumber{value: value, set: true}
	return nil
}

// whole reads the number as an integer code, false unless it is a whole number.
func (n looseNumber) whole() (int, bool) {
	if !n.set || n.value != math.Trunc(n.value) || math.Abs(n.value) > math.MaxInt32 {
		return 0, false
	}
	return int(n.value), true
}

// year reads the number as a start year, 0 unless it is a whole number in a
// plausible range: a junk value must not become the series' year.
func (n looseNumber) year() int {
	if year, ok := n.whole(); ok && year >= 1800 && year <= 2200 {
		return year
	}
	return 0
}

// count reads the number as a positive whole count, 0 when unknown.
func (n looseNumber) count() int {
	if count, ok := n.whole(); ok && count > 0 {
		return count
	}
	return 0
}

// rating reads the number as a score out of outOf, through the shared parser
// so every connector scales and rounds alike.
func (n looseNumber) rating(outOf float64) *float64 {
	if !n.set {
		return nil
	}
	return connectors.ParseRating(strconv.FormatFloat(n.value, 'f', -1, 64), outOf)
}
