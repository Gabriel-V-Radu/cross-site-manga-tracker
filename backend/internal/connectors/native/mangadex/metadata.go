package mangadex

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// mangaMetadataAttributes are the manga record's attributes that describe the
// series rather than locate it. They ride along on the /manga/{id} request the
// resolve makes anyway; the author and artist names come from the same
// request's relationships once it asks for includes[]=author and artist.
//
// Every field is decoded leniently (see looseString and friends): the record is
// what the resolve rests on, so a field that changes shape must cost that field
// alone, never the whole resolve.
type mangaMetadataAttributes struct {
	OriginalLanguage       looseString   `json:"originalLanguage"`
	Status                 looseString   `json:"status"`
	Year                   looseNumber   `json:"year"`
	ContentRating          looseString   `json:"contentRating"`
	PublicationDemographic looseString   `json:"publicationDemographic"`
	Tags                   looseTags     `json:"tags"`
	Description            localizedText `json:"description"`
}

// seriesMetadata maps the record onto the shared vocabulary, nil when it says
// nothing worth storing. MangaDex has no original-format field, so the type is
// inferred from the original language; its tags are grouped, and only the
// genre and theme groups are kept (format tags such as "Long Strip" and content
// warnings such as "Gore" describe neither). The description is taken in
// English or not at all: the panel it feeds is read in English, and a Spanish
// synopsis is not a fallback a reader asked for. The endpoint has no community
// score, so Rating stays nil.
func seriesMetadata(attributes mangaMetadataAttributes, relationships []mangaRelationship) *connectors.SeriesMetadata {
	var genres, tags []string
	for _, tag := range attributes.Tags {
		name := tag.Attributes.Name["en"]
		switch strings.ToLower(strings.TrimSpace(string(tag.Attributes.Group))) {
		case "genre":
			genres = append(genres, name)
		case "theme":
			tags = append(tags, name)
		}
	}

	var authors, artists []string
	for _, relationship := range relationships {
		switch relationship.Type {
		case "author":
			authors = append(authors, string(relationship.Attributes.Name))
		case "artist":
			artists = append(artists, string(relationship.Attributes.Name))
		}
	}

	metadata := &connectors.SeriesMetadata{
		Type:          connectors.SeriesTypeFromLanguage(string(attributes.OriginalLanguage)),
		Status:        connectors.NormalizeSeriesStatus(string(attributes.Status)),
		Year:          attributes.Year.year(),
		Genres:        connectors.CleanLabels(genres),
		Tags:          connectors.CleanLabels(tags),
		Demographic:   demographicLabel(string(attributes.PublicationDemographic)),
		ContentRating: connectors.NormalizeContentRating(string(attributes.ContentRating)),
		Description:   connectors.PlainDescription(attributes.Description["en"]),
		Authors:       connectors.CleanLabels(authors),
		Artists:       connectors.CleanLabels(artists),
	}
	if metadata.Empty() {
		return nil
	}
	return metadata
}

// demographicLabel turns MangaDex's lowercase publicationDemographic
// ("shounen", "seinen") into the capitalised label the other sites use. The
// API's "none" and null both come out empty.
func demographicLabel(raw string) string {
	label := strings.TrimSpace(raw)
	first, size := utf8.DecodeRuneInString(label)
	if size == 0 {
		return ""
	}
	cleaned := connectors.CleanLabels([]string{string(unicode.ToUpper(first)) + label[size:]})
	if len(cleaned) == 0 {
		return ""
	}
	return cleaned[0]
}

// mangaTag is one entry of the record's tags list. Its name is a localized
// string; group is "genre", "theme", "format" or "content".
type mangaTag struct {
	Attributes struct {
		Name  localizedText `json:"name"`
		Group looseString   `json:"group"`
	} `json:"attributes"`
}

// looseTags decodes the tags list entry by entry, skipping any entry that is
// not an object, so one malformed tag cannot take the others with it.
type looseTags []mangaTag

func (tags *looseTags) UnmarshalJSON(data []byte) error {
	*tags = nil
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	for _, entry := range entries {
		var tag mangaTag
		if json.Unmarshal(entry, &tag) == nil {
			*tags = append(*tags, tag)
		}
	}
	return nil
}

// localizedText is MangaDex's LocalizedString, a {"en": "...", "ja": "..."}
// map. An empty one may arrive as [] rather than {} (a PHP-serialiser habit
// Mihon's MangaDex extension also works around), and a value that is not a
// string is dropped, so decoding never fails.
type localizedText map[string]string

func (text *localizedText) UnmarshalJSON(data []byte) error {
	*text = nil
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	decoded := make(localizedText, len(entries))
	for language, raw := range entries {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			decoded[language] = value
		}
	}
	*text = decoded
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

// looseNumber is a JSON number the API may also send as a string or null.
// Anything that does not read as a finite number leaves it unset rather than
// failing the decode.
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

// year reads the number as a start year, 0 unless it is a whole number in a
// plausible range: a junk value must not become the series' year.
func (n looseNumber) year() int {
	if !n.set || n.value != math.Trunc(n.value) || n.value < 1800 || n.value > 2200 {
		return 0
	}
	return int(n.value)
}
