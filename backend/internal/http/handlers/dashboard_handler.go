package handlers

import (
	"database/sql"
	"html/template"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type DashboardHandler struct {
	trackerRepo        *repository.TrackerRepository
	sourceRepo         *repository.SourceRepository
	profileRepo        *repository.ProfileRepository
	profileResolver    *profileContextResolver
	registry           *connectors.Registry
	coverCache         map[string]coverCacheEntry
	cacheMu            sync.RWMutex
	coverFetchMu       sync.Mutex
	coverInFlight      map[string]bool
	coverFetchSem      chan struct{}
	mangafireCoverSem  chan struct{}
	chapterURLCache    map[string]chapterURLCacheEntry
	chapterURLCacheMu  sync.RWMutex
	chapterURLFetchMu  sync.Mutex
	chapterURLInFlight map[string]bool
	chapterURLFetchSem chan struct{}
	activePageMu       sync.RWMutex
	activePageKey      string
	templates          *template.Template
	templateOnce       sync.Once
	templateErr        error
}

type coverCacheEntry struct {
	CoverURL  string
	Found     bool
	ExpiresAt time.Time
}

type chapterURLCacheEntry struct {
	ChapterURL string
	Found      bool
	ExpiresAt  time.Time
}

var allowedTagIconKeys = map[string]bool{
	"icon_1": true,
	"icon_2": true,
	"icon_3": true,
}

var tagIconKeysOrdered = []string{"icon_1", "icon_2", "icon_3"}

type dashboardPageData struct {
	Statuses              []string
	Sorts                 []string
	Profiles              []models.Profile
	ActiveProfile         models.Profile
	RenameValue           string
	ProfileTags           []models.CustomTag
	LinkedSites           []models.Source
	SelectedLinkedSiteIDs map[int64]bool
}

type trackersPartialData struct {
	Trackers      []trackerCardView
	SiteLinks     []trackerSiteLinkView
	ViewMode      string
	Page          int
	PrevPage      int
	NextPage      int
	TotalResults  int
	TotalPages    int
	PageNumbers   []int
	HasPrevPage   bool
	HasNextPage   bool
	PendingCovers bool
	RefreshKey    string
}

type trackerOOBResponseData struct {
	ViewMode        string
	ReplaceCard     *trackerCardView
	PrependCard     *trackerCardView
	DeleteTrackerID int64
}

type trackerCardView struct {
	ID                     int64
	Title                  string
	Status                 string
	StatusLabel            string
	Tags                   []trackerTagView
	HiddenTagCount         int
	TagIcons               []trackerTagIconView
	SourceURL              string
	LatestKnownChapterURL  string
	LastReadChapterURL     string
	CoverURL               string
	SourceLogoURL          string
	SourceLogoLabel        string
	LatestKnownChapter     string
	LatestReleaseAgo       string
	LastCheckedAgo         string
	LastReadChapter        string
	LastReadAgo            string
	RatingLabel            string
	LatestReleaseFormatted string
	UpdatedAtFormatted     string
	LastCheckedFormatted   string
	SourceItemID           *string
	Rating                 *float64
	LatestKnownChapterRaw  *float64
	LastReadChapterRaw     *float64
}

type trackerSiteLinkView struct {
	Name    string
	HomeURL string
	LogoURL string
}

type trackerTagView struct {
	ID       int64
	Name     string
	IconKey  *string
	IconPath *string
}

type trackerTagIconView struct {
	TagName  string
	IconPath string
}

type trackerFormData struct {
	Mode          string
	ViewMode      string
	Tracker       *models.Tracker
	Sources       []models.Source
	LinkedSources []models.TrackerSource
	ProfileTags   []models.CustomTag
	TrackerTags   []models.CustomTag
	TagIconKeys   []string
}

type trackerSearchResultsData struct {
	Items      []connectors.MangaResult
	Query      string
	Error      string
	SourceID   int64
	SourceName string
	Intent     string
}

type profileMenuData struct {
	Profiles          []models.Profile
	ActiveProfile     models.Profile
	RenameValue       string
	LinkedSites       []models.Source
	SourceLogoURLs    map[int64]string
	ProfileTags       []models.CustomTag
	TagIconKeys       []string
	AvailableIconKeys []string
	Message           string
}

type profileFilterTagsData struct {
	ProfileTags []models.CustomTag
}

type profileFilterLinkedSitesData struct {
	LinkedSites       []models.Source
	SelectedSourceIDs map[int64]bool
}

func NewDashboardHandler(db *sql.DB, registry *connectors.Registry) *DashboardHandler {
	if registry == nil {
		registry = connectors.NewRegistry()
	}
	return &DashboardHandler{
		trackerRepo:        repository.NewTrackerRepository(db),
		sourceRepo:         repository.NewSourceRepository(db),
		profileRepo:        repository.NewProfileRepository(db),
		profileResolver:    newProfileContextResolver(db),
		registry:           registry,
		coverCache:         make(map[string]coverCacheEntry),
		coverInFlight:      make(map[string]bool),
		coverFetchSem:      make(chan struct{}, 8),
		mangafireCoverSem:  make(chan struct{}, 3),
		chapterURLCache:    make(map[string]chapterURLCacheEntry),
		chapterURLInFlight: make(map[string]bool),
		chapterURLFetchSem: make(chan struct{}, 10),
	}
}
