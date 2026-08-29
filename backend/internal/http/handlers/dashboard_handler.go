package handlers

import (
	"context"
	"database/sql"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	// coverDir is where resolved covers are downloaded and served from
	// (/covers). Empty disables the local store and falls back to hotlinking
	// the source CDNs — the pre-store behavior tests still exercise.
	coverDir string
	// coverClient fetches candidate cover images. Cover URLs come from scraped
	// third-party pages, so the constructor installs the guarded client, which
	// refuses anything but https to a public address. Nil falls back to the
	// unguarded client: tests build the handler by struct literal and serve
	// their fixtures from 127.0.0.1.
	coverClient        *http.Client
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
	// Cached result of the last connector health sweep, for the link review
	// scope's "broken sources" option — the sweep hits every site at once.
	sourceHealthMu        sync.Mutex
	sourceHealthUnhealthy []int64
	sourceHealthExpires   time.Time
	// The raw scope values of the last scan launched, so reopening the review
	// page shows the slice that scan covered instead of everything unlinked.
	lastLinkScanMu    sync.Mutex
	lastLinkScanScope map[string]string
	templates         *template.Template
	templateErr       error
	// cacheSweepStop ends the background expiry sweep; nil on a handler built
	// by struct literal, which never starts one.
	cacheSweepStop chan struct{}
	cacheSweepOnce sync.Once
}

type coverCacheEntry struct {
	CoverURL string
	Found    bool
	// SourceKey names the source that actually supplied the cover, which is not
	// always the tracker's primary one. The card badge and its "open" link follow
	// it so the UI never claims a site that served nothing.
	SourceKey string
	ExpiresAt time.Time
	// LocalPath names the downloaded copy under coverDir. Non-empty entries
	// serve /covers/{LocalPath} and never expire — the file is the cache.
	LocalPath string
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
	ID      int64
	Name    string
	IconKey *string
	// IconPath is resolved from IconKey here rather than carried up from the
	// repository, which stores keys and knows nothing about /assets. Empty
	// renders a chip with no image.
	IconPath string
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
		coverDir:           filepath.Join("data", "covers"),
		coverClient:        guardedCoverClient,
	}
	// A store that cannot be created only costs the local copies: covers
	// degrade to hotlinking the source CDNs, the pre-store behavior.
	if err := os.MkdirAll(handler.coverDir, 0o755); err != nil {
		slog.Warn("cover directory unavailable; serving covers remotely", "dir", handler.coverDir, "error", err)
		handler.coverDir = ""
	}
	// Parsed here rather than on the first request: a template that does not
	// parse is a deploy defect, and the caller turns it into a startup failure
	// instead of a 500 on every page for as long as the process lives.
	handler.templates, handler.templateErr = parseDashboardTemplates()
	handler.seedCoverCacheFromStore()
	handler.startCacheSweeper()
	return handler
}

// TemplateError reports a template set that did not parse. A process that is
// meant to keep running must refuse to start on it.
func (h *DashboardHandler) TemplateError() error {
	return h.templateErr
}

// cacheSweepInterval paces the sweep below. Nothing it drops is urgent — those
// entries are already dead, merely unreferenced — so the sweep is paced to stay
// invisible on the Pi rather than to reclaim promptly.
const cacheSweepInterval = 15 * time.Minute

// startCacheSweeper drops expired cover and chapter-URL entries in the
// background. Both caches only ever evict on a read of the same key, and the
// keys that go cold are exactly the ones nobody reads again — every chapter
// that ever released, every source URL a tracker was relinked away from — so on
// a process that runs for months they are only ever added to.
func (h *DashboardHandler) startCacheSweeper() {
	h.cacheSweepStop = make(chan struct{})
	stop := h.cacheSweepStop
	go func() {
		ticker := time.NewTicker(cacheSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				h.sweepExpiredCaches()
			}
		}
	}()
}

// Close stops the background sweep. Idempotent, and a no-op on a handler that
// never started one.
func (h *DashboardHandler) Close() {
	h.cacheSweepOnce.Do(func() {
		if h.cacheSweepStop != nil {
			close(h.cacheSweepStop)
		}
	})
}

// sweepExpiredCaches only removes entries that already read as misses, so it
// cannot change what a cached lookup answers. Covers with a local copy are
// exempt for the same reason reads are: the file on disk is the cache.
func (h *DashboardHandler) sweepExpiredCaches() {
	now := time.Now().UTC()

	h.chapterURLCacheMu.Lock()
	for key, entry := range h.chapterURLCache {
		if now.After(entry.ExpiresAt) {
			delete(h.chapterURLCache, key)
		}
	}
	h.chapterURLCacheMu.Unlock()

	h.cacheMu.Lock()
	for key, entry := range h.coverCache {
		if entry.LocalPath == "" && now.After(entry.ExpiresAt) {
			delete(h.coverCache, key)
		}
	}
	h.cacheMu.Unlock()
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
			LocalPath: entry.LocalPath,
		}
	}
	h.cacheMu.Unlock()
	if len(entries) > 0 {
		slog.Info("cover cache warmed from store", "entries", len(entries))
	}
}
