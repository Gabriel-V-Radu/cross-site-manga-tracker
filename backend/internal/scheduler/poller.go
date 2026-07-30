package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type pollRepository interface {
	ListForPolling() ([]repository.PollingTracker, error)
	UpdatePollingState(id int64, sourceID int64, currentSourceURL string, sourceItemID *string, sourceURL string, latestKnownChapter *float64, latestReleaseAt *time.Time, clearLatestReleaseAt bool, checkedAt time.Time) error
}

type Poller struct {
	repo         pollRepository
	registry     *connectors.Registry
	interval     time.Duration
	idleInterval time.Duration
	logger       *slog.Logger
	stopCh       chan struct{}
}

type PollerConfig struct {
	Interval time.Duration
	// IdleInterval is the minimum time between polls for trackers that are
	// not in "reading" status; they rarely change, so polling them every
	// cycle just burns the sources' rate limits.
	IdleInterval time.Duration
}

func NewPoller(repo pollRepository, registry *connectors.Registry, cfg PollerConfig, logger *slog.Logger) *Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Minute
	}
	if cfg.IdleInterval <= 0 {
		cfg.IdleInterval = 12 * time.Hour
	}
	if cfg.IdleInterval < cfg.Interval {
		cfg.IdleInterval = cfg.Interval
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Poller{
		repo:         repo,
		registry:     registry,
		interval:     cfg.Interval,
		idleInterval: cfg.IdleInterval,
		logger:       logger,
		stopCh:       make(chan struct{}),
	}
}

func (p *Poller) Start(ctx context.Context) {
	p.logger.Info("poller started", "interval", p.interval.String())
	ticker := time.NewTicker(p.interval)
	go func() {
		defer ticker.Stop()
		if err := p.RunOnce(ctx); err != nil {
			p.logger.Warn("poller initial run failed", "error", err)
		}
		for {
			select {
			case <-ctx.Done():
				p.logger.Info("poller stopped")
				close(p.stopCh)
				return
			case <-ticker.C:
				if err := p.RunOnce(ctx); err != nil {
					p.logger.Warn("poller cycle failed", "error", err)
				}
			}
		}
	}()
}

func (p *Poller) StopWait(timeout time.Duration) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	select {
	case <-p.stopCh:
	case <-time.After(timeout):
	}
}

func (p *Poller) RunOnce(ctx context.Context) error {
	trackers, err := p.repo.ListForPolling()
	if err != nil {
		return fmt.Errorf("load trackers for polling: %w", err)
	}

	skippedIdle := 0
	for _, tracker := range trackers {
		if p.shouldSkipIdle(tracker) {
			skippedIdle++
			continue
		}

		connector, ok := p.registry.Get(tracker.SourceKey)
		if !ok {
			p.logger.Debug("connector missing for tracker", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey)
			continue
		}

		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, resolveErr := connector.ResolveByURL(requestCtx, tracker.SourceURL)
		cancel()

		// A source can go dark for reasons no retry fixes — a site put behind an
		// interactive bot challenge, a domain that moved. When that happens, poll
		// the tracker's other linked sources so the chapter count keeps advancing
		// from whichever mirror is still readable.
		usedFallback := false
		if resolveErr != nil {
			if fallbackResult, fallbackSource := p.resolveFromAlternates(ctx, tracker); fallbackResult != nil {
				p.logger.Info("poll fell back to alternate source",
					"trackerId", tracker.ID,
					"primarySourceKey", tracker.SourceKey,
					"fallbackSourceKey", fallbackSource.SourceKey,
					"primaryError", resolveErr)
				result, resolveErr = fallbackResult, nil
				usedFallback = true
			}
		}

		if resolveErr != nil {
			p.logger.Warn("poll resolve failed", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey, "error", resolveErr)
			continue
		}

		now := time.Now().UTC()
		latest := tracker.LatestKnownChapter
		if result.LatestChapter != nil {
			latest = result.LatestChapter
		}
		if usedFallback && !isNewChapter(tracker.LatestKnownChapter, result.LatestChapter) {
			// Mirrors routinely lag the primary source. Letting a fallback lower
			// latest_known_chapter would resurrect chapters the user already read,
			// so a fallback may advance the count but never walk it back.
			latest = tracker.LatestKnownChapter
		}

		newChapter := isNewChapter(tracker.LatestKnownChapter, result.LatestChapter)
		latestReleaseAt := result.LastUpdatedAt
		if !newChapter && tracker.LatestReleaseAt != nil {
			// The chapter didn't advance, so keep the stored release date
			// rather than rewriting it with the source's current value —
			// some sources bump their update timestamp without releasing
			// anything, which made dates drift toward "just now".
			latestReleaseAt = nil
		}
		clearLatestReleaseAt := latestReleaseAt == nil && newChapter

		var canonicalSourceItemID *string
		resolvedSourceItemID := strings.TrimSpace(result.SourceItemID)
		if resolvedSourceItemID != "" {
			canonicalSourceItemID = &resolvedSourceItemID
		} else {
			canonicalSourceItemID = tracker.SourceItemID
		}
		canonicalSourceURL := strings.TrimSpace(result.URL)
		if canonicalSourceURL == "" {
			canonicalSourceURL = tracker.SourceURL
		}
		sourceID := tracker.SourceID
		currentSourceURL := tracker.SourceURL

		if usedFallback {
			// The chapter number carries across sources; the identifiers do not.
			// Writing the fallback's id/URL here would silently repoint the
			// tracker's primary source at the mirror while source_id still names
			// the original, so a fallback poll updates progress only and leaves
			// the primary pointer for the user to change deliberately.
			canonicalSourceItemID = nil
			canonicalSourceURL = ""
			sourceID = 0
			currentSourceURL = ""
		}

		if err := p.repo.UpdatePollingState(tracker.ID, sourceID, currentSourceURL, canonicalSourceItemID, canonicalSourceURL, latest, latestReleaseAt, clearLatestReleaseAt, now); err != nil {
			p.logger.Warn("poll update state failed", "trackerId", tracker.ID, "error", err)
			continue
		}
	}

	if skippedIdle > 0 {
		p.logger.Debug("poll skipped idle trackers", "count", skippedIdle)
	}

	return nil
}

// resolveFromAlternates tries the tracker's non-primary linked sources in order
// and returns the first that resolves, along with the source it came from. It
// returns (nil, zero) when the tracker has no alternates or none of them answer,
// leaving the caller to report the primary source's error.
func (p *Poller) resolveFromAlternates(ctx context.Context, tracker repository.PollingTracker) (*connectors.MangaResult, repository.PollingTrackerSource) {
	for _, source := range tracker.AlternateSources {
		if strings.TrimSpace(source.SourceURL) == "" {
			continue
		}

		connector, ok := p.registry.Get(source.SourceKey)
		if !ok {
			continue
		}

		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := connector.ResolveByURL(requestCtx, source.SourceURL)
		cancel()

		if err != nil {
			p.logger.Debug("poll fallback source failed",
				"trackerId", tracker.ID, "sourceKey", source.SourceKey, "error", err)
			continue
		}
		if result != nil {
			return result, source
		}
	}

	return nil, repository.PollingTrackerSource{}
}

// shouldSkipIdle reports whether a non-reading tracker was checked recently
// enough that this cycle can skip it.
func (p *Poller) shouldSkipIdle(tracker repository.PollingTracker) bool {
	if strings.EqualFold(strings.TrimSpace(tracker.Status), "reading") {
		return false
	}
	if tracker.LastCheckedAt == nil {
		return false
	}
	return time.Since(*tracker.LastCheckedAt) < p.idleInterval
}

func isNewChapter(previous *float64, current *float64) bool {
	if current == nil {
		return false
	}
	if previous == nil {
		return true
	}
	return *current > *previous
}
