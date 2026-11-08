package mangafire

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// apiTitleDetail is the record /api/titles/{hid} answers with: the fields a
// search row shares (apiTitle) plus what the site says about the series. Only
// the detail record is read for metadata; search rows carry some of the same
// fields, but MangaResult.Metadata is a resolve-only field.
//
// Every metadata field is decoded leniently (see looseString and friends): the
// title record is what the resolve rests on, so a field that changes shape — a
// number sent as a string, an object where a list was — must cost that field
// alone, never the whole resolve.
type apiTitleDetail struct {
	apiTitle

	Type          looseString `json:"type"`
	Status        looseString `json:"status"`
	Year          looseNumber `json:"year"`
	ContentRating looseString `json:"contentRating"`
	Rating        looseNumber `json:"rating"`
	RatingCount   looseNumber `json:"ratingCount"`
	SynopsisHTML  looseString `json:"synopsisHtml"`
	Genres        looseLabels `json:"genres"`
	Themes        looseLabels `json:"themes"`
	Demographics  looseLabels `json:"demographics"`
	Authors       looseLabels `json:"authors"`
	Artists       looseLabels `json:"artists"`
}

// metadata maps the detail record onto the shared vocabulary, nil when the
// record says nothing worth storing. MangaFire keeps genres and themes apart,
// so themes become Tags; its rating is already on a 0-10 scale.
func (detail apiTitleDetail) metadata() *connectors.SeriesMetadata {
	metadata := &connectors.SeriesMetadata{
		Type:          connectors.NormalizeSeriesType(string(detail.Type)),
		Status:        connectors.NormalizeSeriesStatus(string(detail.Status)),
		Year:          detail.Year.year(),
		Genres:        connectors.CleanLabels(detail.Genres),
		Tags:          connectors.CleanLabels(detail.Themes),
		ContentRating: connectors.NormalizeContentRating(string(detail.ContentRating)),
		Description:   connectors.PlainDescription(string(detail.SynopsisHTML)),
		Authors:       connectors.CleanLabels(detail.Authors),
		Artists:       connectors.CleanLabels(detail.Artists),
		Rating:        detail.Rating.rating(10),
	}
	if demographics := connectors.CleanLabels(detail.Demographics); len(demographics) > 0 {
		metadata.Demographic = demographics[0]
	}
	if metadata.Rating != nil {
		metadata.RatingVotes = detail.RatingCount.count()
	}
	if metadata.Empty() {
		return nil
	}
	return metadata
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

// looseNumber is a JSON number the API may also send as a string ("9.4") or
// null. Anything that does not read as a finite number leaves it unset rather
// than failing the decode.
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

// count reads the number as a positive whole count, 0 when unknown.
func (n looseNumber) count() int {
	if !n.set || n.value != math.Trunc(n.value) || n.value <= 0 || n.value > math.MaxInt32 {
		return 0
	}
	return int(n.value)
}

// rating reads the number as a score out of outOf, through the shared parser
// so every connector scales and rounds alike.
func (n looseNumber) rating(outOf float64) *float64 {
	if !n.set {
		return nil
	}
	return connectors.ParseRating(strconv.FormatFloat(n.value, 'f', -1, 64), outOf)
}

// looseLabels decodes MangaFire's [{"id":5,"title":"Comedy"}] lists into their
// titles. A plain string entry is taken as is, an entry of any other shape is
// skipped, and a value that is not a list at all decodes as empty.
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
			Title looseString `json:"title"`
			Name  looseString `json:"name"`
		}
		if json.Unmarshal(entry, &named) != nil {
			continue
		}
		if label := strings.TrimSpace(string(named.Title)); label != "" {
			*labels = append(*labels, label)
		} else if label := strings.TrimSpace(string(named.Name)); label != "" {
			*labels = append(*labels, label)
		}
	}
	return nil
}
