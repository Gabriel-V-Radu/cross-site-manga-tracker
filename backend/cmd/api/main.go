package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	apihttp "github.com/gabriel/cross-site-tracker/backend/internal/http"
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
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return 1
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	db, err := database.Open(cfg.SQLitePath)
	if err != nil {
		slog.Error("failed to open sqlite", "path", cfg.SQLitePath, "error", err)
		return 1
	}
	defer db.Close()

	if err := database.ApplyMigrations(db, cfg.MigrationsPath); err != nil {
		slog.Error("failed to apply migrations", "error", err)
		return 1
	}

	if cfg.SeedDefaultData {
		if err := database.SeedDefaults(db); err != nil {
			slog.Error("failed to seed defaults", "error", err)
			return 1
		}
	}

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
	poller := scheduler.NewPoller(
		repository.NewTrackerRepository(db),
		connectorRegistry,
		scheduler.PollerConfig{
			Interval:     time.Duration(cfg.PollingMinutes) * time.Minute,
			IdleInterval: time.Duration(cfg.PollingIdleMinutes) * time.Minute,
		},
		slog.Default(),
	)
	if cfg.PollingEnabled {
		poller.Start(pollerCtx)
	}

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
	}
	pollerCancel()
	poller.StopWait(2 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	return exitCode
}
