package handlers

import (
	"database/sql"
	"html/template"
	"sync"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/linkscan"
	"github.com/gabriel/cross-site-tracker/backend/internal/mangabaka"
	"github.com/gabriel/cross-site-tracker/backend/internal/models"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/resolve"
)

type DashboardHandler struct {
	trackerRepo        *repository.TrackerRepository
	sourceRepo         *repository.SourceRepository
	profileRepo        *repository.ProfileRepository
	linkSuggestionRepo *repository.LinkSuggestionRepository
	linkScanner        *linkscan.Scanner
	profileResolver    *profileContextResolver
	registry           *connectors.Registry
	// The background resolution service behind every card: what art to show and
	// where each chapter opens. Both are held as interfaces so the card builder
	// can be exercised against fixed answers — what a card renders is a separate
	// question from what the network came back with.
	covers       coverResolver
	chapterLinks chapterLinkResolver
	// pageKeys abandons a background lookup queued for a trackers page the
	// reader has since navigated away from. The two resolvers share it: there is
	// only ever one such page.
	pageKeys *resolve.PageGate
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
	// Where this handler reads its templates and writes its uploads. Held
	// rather than spelled out at each call site so nothing depends on the
	// process's working directory.
	paths  DashboardPaths
	assets *assetVersions
}

// DashboardPaths are the filesystem locations the dashboard depends on. They
// come from the config so a deployment can put them anywhere; the zero value
// leaves each one to whatever "" resolves to, which is only ever useful to a
// test that touches none of them.
type DashboardPaths struct {
	// TemplatesGlob matches the template files to parse.
	TemplatesGlob string
	// AssetsDir is the directory the /assets route serves, read here only to
	// stamp asset URLs with the file's modification time.
	AssetsDir string
	// CoversDir must be the directory the /covers route serves: the cover
	// resolver stores files there and mints /covers/ hrefs for them.
	CoversDir string
	// SourceLogosDir must sit under the directory the /uploads route serves,
	// for the same reason.
	SourceLogosDir string
}

// coverResolver is the cover half of the resolution service (internal/resolve).
type coverResolver interface {
	Lookup(sourceKey, sourceURL string, sourceItemID *string, alternates []repository.TrackerSourceRef, pageKey string) (coverURL string, servingSourceKey string, waiting bool)
	InvalidateNegatives()
	Close()
}

// chapterLinkResolver is the chapter-link half of the same service.
type chapterLinkResolver interface {
	Lookup(sourceKey, sourceURL string, chapter float64, alternates []repository.TrackerSourceRef, pageKey string) (chapterURL string, resolved bool, waiting bool)
	Invalidate()
	Close()
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

func NewDashboardHandler(db *sql.DB, registry *connectors.Registry, paths DashboardPaths) *DashboardHandler {
	if registry == nil {
		registry = connectors.NewRegistry()
	}
	linkSuggestionRepo := repository.NewLinkSuggestionRepository(db)
	pageKeys := resolve.NewPageGate()
	handler := &DashboardHandler{
		trackerRepo:        repository.NewTrackerRepository(db),
		sourceRepo:         repository.NewSourceRepository(db),
		profileRepo:        repository.NewProfileRepository(db),
		linkSuggestionRepo: linkSuggestionRepo,
		linkScanner:        linkscan.NewScanner(linkSuggestionRepo, registry, mangabaka.NewClient(), nil),
		profileResolver:    newProfileContextResolver(db),
		registry:           registry,
		pageKeys:           pageKeys,
		paths:              paths,
		assets:             newAssetVersions(paths.AssetsDir),
		covers: resolve.NewCoverResolver(resolve.CoverConfig{
			Registry: registry,
			Store:    repository.NewCoverCacheRepository(db),
			// Where the router mounts the static /covers route; the two names
			// the same directory or the browser is handed hrefs to nothing.
			Dir:  paths.CoversDir,
			Gate: pageKeys,
		}),
		chapterLinks: resolve.NewChapterLinkResolver(resolve.ChapterConfig{
			Registry: registry,
			Gate:     pageKeys,
		}),
	}
	// Parsed here rather than on the first request: a template that does not
	// parse is a deploy defect, and the caller turns it into a startup failure
	// instead of a 500 on every page for as long as the process lives.
	handler.templates, handler.templateErr = parseDashboardTemplates(paths.TemplatesGlob, handler.assets.url)
	return handler
}

// TemplateError reports a template set that did not parse. A process that is
// meant to keep running must refuse to start on it.
func (h *DashboardHandler) TemplateError() error {
	return h.templateErr
}

// Close stops the resolvers' background cache sweeps and cancels any running
// link scan. The router hangs it off the app's shutdown hook so a shut-down
// app — every one a test binary builds — does not leave the goroutines behind,
// and so a scan cannot outlive the process that owns its database. Idempotent.
func (h *DashboardHandler) Close() {
	h.covers.Close()
	h.chapterLinks.Close()
	if h.linkScanner != nil {
		h.linkScanner.Close()
	}
}
