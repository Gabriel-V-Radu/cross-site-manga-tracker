package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/notifications"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

type pollRepository interface {
	ListForPolling(statuses []string) ([]repository.PollingTracker, error)
	UpdatePollingState(id int64, latestKnownChapter *float64, latestReleaseAt *time.Time, checkedAt time.Time) error
}

type Poller struct {
	repo          pollRepository
	registry      *connectors.Registry
	notifier      notifications.Notifier
	interval      time.Duration
	notifyEnabled bool
	notifyMap     map[string]bool
	logger        *slog.Logger
	stopCh        chan struct{}
}

type PollerConfig struct {
	Interval      time.Duration
	NotifyEnabled bool
	NotifyStatus  []string
}

func NewPoller(repo pollRepository, registry *connectors.Registry, notifier notifications.Notifier, cfg PollerConfig, logger *slog.Logger) *Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	if notifier == nil {
		notifier = notifications.NoopNotifier{}
	}

	notifyMap := make(map[string]bool)
	for _, status := range cfg.NotifyStatus {
		notifyMap[status] = true
	}

	return &Poller{
		repo:          repo,
		registry:      registry,
		notifier:      notifier,
		interval:      cfg.Interval,
		notifyEnabled: cfg.NotifyEnabled,
		notifyMap:     notifyMap,
		logger:        logger,
		stopCh:        make(chan struct{}),
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
	statuses := make([]string, 0)
	for status := range p.notifyMap {
		statuses = append(statuses, status)
	}

	trackers, err := p.repo.ListForPolling(nil)
	if err != nil {
		return fmt.Errorf("load trackers for polling: %w", err)
	}

	for _, tracker := range trackers {
		connector, ok := p.registry.Get(tracker.SourceKey)
		if !ok {
			p.logger.Debug("connector missing for tracker", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey)
			continue
		}

		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, resolveErr := connector.ResolveByURL(requestCtx, tracker.SourceURL)
		cancel()

		if resolveErr != nil {
			p.logger.Warn("poll resolve failed", "trackerId", tracker.ID, "sourceKey", tracker.SourceKey, "error", resolveErr)
			continue
		}

		now := time.Now().UTC()
		latest := tracker.LatestKnownChapter
		if result.LatestChapter != nil {
			latest = result.LatestChapter
		}

		if err := p.repo.UpdatePollingState(tracker.ID, latest, result.LastUpdatedAt, now); err != nil {
			p.logger.Warn("poll update state failed", "trackerId", tracker.ID, "error", err)
			continue
		}

		if !p.notifyEnabled || result.LatestChapter == nil {
			continue
		}
		if !p.notifyMap[tracker.Status] {
			continue
		}
		if !isNewChapter(tracker.LatestKnownChapter, result.LatestChapter) {
			continue
		}

		notifyCtx, notifyCancel := context.WithTimeout(ctx, 5*time.Second)
		notifyErr := p.notifier.Notify(notifyCtx, notifications.Message{
			Title: "New chapter available",
			Body:  fmt.Sprintf("%s now has chapter %.2f", tracker.Title, *result.LatestChapter),
			Context: map[string]interface{}{
				"trackerId":         tracker.ID,
				"title":             tracker.Title,
				"status":            tracker.Status,
				"source":            tracker.SourceKey,
				"sourceUrl":         tracker.SourceURL,
				"latestKnownBefore": tracker.LatestKnownChapter,
				"latestKnownNow":    result.LatestChapter,
			},
		})
		notifyCancel()
		if notifyErr != nil {
			p.logger.Warn("notification failed", "trackerId", tracker.ID, "error", notifyErr)
		}
	}

	return nil
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
