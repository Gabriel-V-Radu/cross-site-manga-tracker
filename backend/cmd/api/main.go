package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/app"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
	"github.com/gabriel/cross-site-tracker/backend/internal/metadata"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/scheduler"
)

// main only translates run's verdict into an exit code. Every os.Exit used to
// sit inside the body, past `defer db.Close()`, which a direct exit skips: the
// "server stopped unexpectedly" path left SQLite unclosed and its -wal/-shm
// behind — the case restore.ps1 has to defend against.
func main() {
	os.Exit(run())
}

func run() int {
	// The server is the one binary that always migrates: it owns the database
	// and a schema behind the code it runs is not a state it can serve from.
	runtime, err := app.Open(app.Options{Migrate: true, JSONLogs: true})
	if err != nil {
		slog.Error("startup failed", "error", err)
		return 1
	}
	defer runtime.Close()
	cfg, db := runtime.Config, runtime.DB

	connectorRegistry := connectordefaults.NewRegistry()

	// A template that does not parse must stop the deploy here: the container
	// restarts on a non-zero exit, and the alternative is a process that comes
	// up healthy and answers every page with a 500 until someone notices.
	app, err := apihttp.BuildServer(cfg, db, connectorRegistry)
	if err != nil {
		slog.Error("failed to build http server", "error", err)
		return 1
	}

	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	// The series metadata the poller's resolves carry is stored and merged
	// per tracker by this service. Its backfill asks sites directly, so it
	// only runs where polling is on; the views are kept current either way.
	metadataService := metadata.NewService(
		repository.NewMetadataRepository(db),
		connectorRegistry,
		metadata.Config{Backfill: cfg.PollingEnabled},
		slog.Default(),
	)
	metadataService.Start(pollerCtx)
	poller := scheduler.NewPoller(
		repository.NewTrackerRepository(db),
		connectorRegistry,
		scheduler.PollerConfig{
			Interval:     time.Duration(cfg.PollingMinutes) * time.Minute,
			IdleInterval: time.Duration(cfg.PollingIdleMinutes) * time.Minute,
			FeedInterval: time.Duration(cfg.FeedMinutes) * time.Minute,
			Metadata:     metadataService,
		},
		slog.Default(),
	)
	if cfg.PollingEnabled {
		poller.Start(pollerCtx)
	}

	// Cancelled by the defers on the way out, before the database closes, so
	// the watch never pings a closed pool.
	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
	defer watchdogCancel()
	dbWedged := watchDatabase(watchdogCtx, db.PingContext, dbWatchdogInterval, dbWatchdogTimeout, dbWatchdogMaxFailures)

	// Listen returns nil on graceful Shutdown, so anything on this channel is a
	// real failure (port taken, socket error). Without it the process would keep
	// running headless — poller alive, no HTTP — and Docker would never restart it.
	serverErr := make(chan error, 1)
	go func() {
		if err := app.Listen(":" + cfg.Port); err != nil {
			serverErr <- err
		}
	}()

	slog.Info("api started", "port", cfg.Port, "env", cfg.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	exitCode := 0
	select {
	case <-ctx.Done():
		slog.Info("shutting down server")
	case err := <-serverErr:
		slog.Error("server stopped unexpectedly", "error", err)
		exitCode = 1
	case err := <-dbWedged:
		slog.Error("database unreachable, shutting down so the container restarts", "error", err, "consecutive_failures", dbWatchdogMaxFailures)
		exitCode = 1
	}
	pollerCancel()
	poller.StopWait(2 * time.Second)
	metadataService.StopWait(2 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	return exitCode
}

// A ping waits for the pool's one connection, so it fails only when that
// connection stayed held for the whole timeout. Five of those a minute apart
// is a connection stuck for five minutes, not a slow write or a busy scan.
const (
	dbWatchdogInterval    = time.Minute
	dbWatchdogTimeout     = 10 * time.Second
	dbWatchdogMaxFailures = 5
)

// watchDatabase pings the database every interval and reports on the returned
// channel once maxFailures pings in a row have failed; a success resets the
// count. Docker's healthcheck cannot do this: plain Engine and compose only
// label a container unhealthy and never restart it, so a process whose single
// SQLite connection was stuck behind a lost transaction kept listening, and
// serving nothing, until someone SSHed in. run treats the report like a dead
// listener — the normal shutdown path, then a non-zero exit, which is what
// `restart: unless-stopped` acts on. The report is buffered so the goroutine
// never blocks on a run that has already moved on, and the watch ends with
// ctx.
func watchDatabase(ctx context.Context, ping func(context.Context) error, interval, timeout time.Duration, maxFailures int) <-chan error {
	wedged := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			pingCtx, cancel := context.WithTimeout(ctx, timeout)
			err := ping(pingCtx)
			cancel()
			if err == nil {
				failures = 0
				continue
			}
			// A ping cut short by shutdown says nothing about the database.
			if ctx.Err() != nil {
				return
			}
			failures++
			slog.Warn("database ping failed", "error", err, "consecutive_failures", failures)
			if failures >= maxFailures {
				wedged <- err
				return
			}
		}
	}()
	return wedged
}
