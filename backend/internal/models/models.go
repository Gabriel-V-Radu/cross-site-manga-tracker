package models

import "time"

type Source struct {
	ID            int64     `json:"id"`
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	ConnectorKind string    `json:"connectorKind"`
	BaseURL       *string   `json:"baseUrl,omitempty"`
	ConfigPath    *string   `json:"configPath,omitempty"`
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
	ID                 int64       `json:"id"`
	ProfileID          int64       `json:"profileId"`
	Title              string      `json:"title"`
	RelatedTitles      []string    `json:"relatedTitles,omitempty"`
	SourceID           int64       `json:"sourceId"`
	SourceItemID       *string     `json:"sourceItemId,omitempty"`
	SourceURL          string      `json:"sourceUrl"`
	Status             string      `json:"status"`
	LastReadChapter    *float64    `json:"lastReadChapter,omitempty"`
	Rating             *float64    `json:"rating,omitempty"`
	LastReadAt         *time.Time  `json:"lastReadAt,omitempty"`
	LatestKnownChapter *float64    `json:"latestKnownChapter,omitempty"`
	LatestReleaseAt    *time.Time  `json:"latestReleaseAt,omitempty"`
	LastCheckedAt      *time.Time  `json:"lastCheckedAt,omitempty"`
	Tags               []CustomTag `json:"tags,omitempty"`
	CreatedAt          time.Time   `json:"createdAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}

type CustomTag struct {
	ID        int64     `json:"id"`
	ProfileID int64     `json:"profileId"`
	Name      string    `json:"name"`
	IconKey   *string   `json:"iconKey,omitempty"`
	IconPath  *string   `json:"iconPath,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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

type Chapter struct {
	ID            int64      `json:"id"`
	TrackerID     int64      `json:"trackerId"`
	ChapterNumber *float64   `json:"chapterNumber,omitempty"`
	ChapterLabel  *string    `json:"chapterLabel,omitempty"`
	ChapterURL    *string    `json:"chapterUrl,omitempty"`
	ReleasedAt    *time.Time `json:"releasedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}
