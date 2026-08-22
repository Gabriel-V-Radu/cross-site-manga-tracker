package handlers

import (
	"context"
	"database/sql"
	"html/template"
	"log/slog"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/linkscan"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type DashboardHandler struct {
	trackerRepo        *repository.TrackerRepository
	sourceRepo         *repository.SourceRepository
	profileRepo        *repository.ProfileRepository
	linkSuggestionRepo *repository.LinkSuggestionRepository
	linkScanner        *linkscan.Scanner
	profileResolver    *profileContextResolver
	registry           *connectors.Registry
	coverCache         map[string]coverCacheEntry
	// coverStore persists cover entries across restarts (nil in tests that
	// build the handler by struct literal). The map above stays the hot path;
	// the store is write-through and only read once, at construction.
	coverStore *repository.CoverCacheRepository
	// coverURLChecker reports whether a cover image URL actually answers.
	// Nil means the real HTTP probe; tests inject their own. It exists
	// because a connector can resolve a syntactically fine cover URL whose
	// CDN is dead (ComicK's meo host, 2026-08), and caching that URL for 12h
	// leaves a card broken with working alternates one hop away.
	coverURLChecker func(ctx context.Context, coverURL string) bool
	cacheMu         sync.RWMutex
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
	// Cached result of the last connector health sweep, for the link review
	// scope's "broken sources" option — the sweep hits every site at once.
	sourceHealthMu        sync.Mutex
	sourceHealthUnhealthy []int64
	sourceHealthExpires   time.Time
	// The raw scope values of the last scan launched, so reopening the review
	// page shows the slice that scan covered instead of everything unlinked.
	lastLinkScanMu    sync.Mutex
	lastLinkScanScope map[string]string
	templates          *template.Template
	templateOnce       sync.Once
	templateErr        error
}

type coverCacheEntry struct {
	CoverURL string
	Found    bool
	// SourceKey names the source that actually supplied the cover, which is not
	// always the tracker's primary one. The card badge and its "open" link follow
	// it so the UI never claims a site that served nothing.
	SourceKey string
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
	MoreSiteLinks []trackerSiteLinkView
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
	ID                    int64
	Title                 string
	Status                string
	StatusLabel           string
	Tags                  []trackerTagView
	HiddenTagCount        int
	TagIcons              []trackerTagIconView
	SourceURL             string
	LatestKnownChapterURL string
	LastReadChapterURL    string
	// HighlightURL is where the card's open-to-read button goes: the pinned
	// reading site, or the site the latest-chapter link resolved to — not
	// blindly the primary source, which may be a tracking-only site.
	HighlightURL string
	// Which site each chapter link actually opens. A card's links can land on
	// different sites when one of them is unreadable, so each says where it goes
	// rather than leaving the user to discover it by clicking.
	LatestKnownChapterSite string
	LastReadChapterSite    string
	CoverURL               string
	SourceLogoURL          string
	SourceLogoLabel        string
	LatestKnownChapter     string
	LatestReleaseAgo       string
	// LatestReleaseApproximate marks a release date the source never reported:
	// the card is showing when this app first saw the chapter instead, so it says
	// so rather than passing a detection time off as a release time.
	LatestReleaseApproximate bool
	LatestReleaseTitle       string
	LastCheckedAgo           string
	LastReadChapter          string
	LastReadAgo              string
	RatingLabel              string
	LatestReleaseFormatted   string
	UpdatedAtFormatted       string
	LastCheckedFormatted     string
	SourceItemID             *string
	Rating                   *float64
	LatestKnownChapterRaw    *float64
	LastReadChapterRaw       *float64
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
	// ReadingOptions are the linked sources deduplicated by site, for the
	// "reading site" selector; ReadingSourceID is the current pin (0 = auto).
	ReadingOptions  []models.TrackerSource
	ReadingSourceID int64
	ProfileTags     []models.CustomTag
	TrackerTags     []models.CustomTag
	TagIconKeys     []string
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
	linkSuggestionRepo := repository.NewLinkSuggestionRepository(db)
	handler := &DashboardHandler{
		trackerRepo:        repository.NewTrackerRepository(db),
		sourceRepo:         repository.NewSourceRepository(db),
		profileRepo:        repository.NewProfileRepository(db),
		linkSuggestionRepo: linkSuggestionRepo,
		linkScanner:        linkscan.NewScanner(linkSuggestionRepo, registry, mangabaka.NewClient(), nil),
		profileResolver:    newProfileContextResolver(db),
		registry:           registry,
		coverCache:         make(map[string]coverCacheEntry),
		coverStore:         repository.NewCoverCacheRepository(db),
		coverInFlight:      make(map[string]bool),
		coverFetchSem:      make(chan struct{}, 8),
		mangafireCoverSem:  make(chan struct{}, 3),
		chapterURLCache:    make(map[string]chapterURLCacheEntry),
		chapterURLInFlight: make(map[string]bool),
		chapterURLFetchSem: make(chan struct{}, 10),
	}
	handler.seedCoverCacheFromStore()
	return handler
}

// seedCoverCacheFromStore warms the in-memory cover cache from the persisted
// one. A failure only costs the warm start, so it is logged and swallowed.
func (h *DashboardHandler) seedCoverCacheFromStore() {
	if h.coverStore == nil {
		return
	}
	entries, err := h.coverStore.LoadFresh()
	if err != nil {
		slog.Warn("cover cache load failed; starting cold", "error", err)
		return
	}
	h.cacheMu.Lock()
	for _, entry := range entries {
		h.coverCache[entry.CacheKey] = coverCacheEntry{
			CoverURL:  entry.CoverURL,
			Found:     entry.Found,
			SourceKey: entry.SourceKey,
			ExpiresAt: entry.ExpiresAt,
		}
	}
	h.cacheMu.Unlock()
	if len(entries) > 0 {
		slog.Info("cover cache warmed from store", "entries", len(entries))
	}
}
