package models

import (
	"strings"
	"time"
)

type Source struct {
	ID            int64     `json:"id"`
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	ConnectorKind string    `json:"connectorKind"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Profile struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Tracker struct {
	ID                 int64      `json:"id"`
	ProfileID          int64      `json:"profileId"`
	Title              string     `json:"title"`
	RelatedTitles      []string   `json:"relatedTitles,omitempty"`
	SourceID           int64      `json:"sourceId"`
	SourceItemID       *string    `json:"sourceItemId,omitempty"`
	SourceURL          string     `json:"sourceUrl"`
	Status             string     `json:"status"`
	LastReadChapter    *float64   `json:"lastReadChapter,omitempty"`
	Rating             *float64   `json:"rating,omitempty"`
	LastReadAt         *time.Time `json:"lastReadAt,omitempty"`
	LatestKnownChapter *float64   `json:"latestKnownChapter,omitempty"`
	LatestReleaseAt    *time.Time `json:"latestReleaseAt,omitempty"`
	// LatestChapterSeenAt is when this app first recorded the chapter number it
	// currently holds. It stands in for the release date on the sources that
	// report a chapter number without one, which is what keeps such a tracker
	// from being ranked by when it was last polled.
	LatestChapterSeenAt *time.Time `json:"latestChapterSeenAt,omitempty"`
	// LatestChapterSourceID names the source that reported the stored latest
	// chapter number, nil until a poll confirms it. When no reading site can
	// resolve a chapter link, the card attributes the number to this source
	// rather than to whoever supplied the cover art.
	LatestChapterSourceID *int64     `json:"latestChapterSourceId,omitempty"`
	LastCheckedAt         *time.Time `json:"lastCheckedAt,omitempty"`
	// ReadingSourceID pins the tracker's reading links to one linked source;
	// nil lets the dashboard pick the best available site.
	ReadingSourceID *int64      `json:"readingSourceId,omitempty"`
	Tags            []CustomTag `json:"tags,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
}

type CustomTag struct {
	ID        int64     `json:"id"`
	ProfileID int64     `json:"profileId"`
	Name      string    `json:"name"`
	IconKey   *string   `json:"iconKey,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IconPath is the image a tag chip renders, derived from the stored key rather
// than stored beside it: the database keeps only the key, so an icon can be
// renamed or re-pointed without a migration and without the repository knowing
// which URLs the web server publishes.
func (t CustomTag) IconPath() string {
	if t.IconKey == nil {
		return ""
	}
	return TagIconAssetPath(*t.IconKey)
}

// TagIconAssetPath maps a tag icon key to the static file the web layer serves
// it from. An unknown key resolves to nothing, which renders a chip with no
// image instead of a broken one. This is the only copy of the mapping: the
// browser used to carry its own, and now reads these paths from the JSON the
// tracker form renders, so an icon can be renamed here alone.
func TagIconAssetPath(iconKey string) string {
	switch strings.TrimSpace(iconKey) {
	case "icon_1":
		return "/assets/tag-icons/icon-star-gold.svg"
	case "icon_2":
		return "/assets/tag-icons/icon-red-heart.svg"
	case "icon_3":
		return "/assets/tag-icons/icon-flames.svg"
	default:
		return ""
	}
}

type TrackerSource struct {
	ID           int64     `json:"id"`
	TrackerID    int64     `json:"trackerId"`
	SourceID     int64     `json:"sourceId"`
	SourceName   string    `json:"sourceName,omitempty"`
	SourceItemID *string   `json:"sourceItemId,omitempty"`
	SourceURL    string    `json:"sourceUrl"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
