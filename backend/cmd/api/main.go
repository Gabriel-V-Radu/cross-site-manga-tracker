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

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	db, err := database.Open(cfg.SQLitePath)
	if err != nil {
		slog.Error("failed to open sqlite", "path", cfg.SQLitePath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := database.ApplyMigrations(db, cfg.MigrationsPath); err != nil {
		slog.Error("failed to apply migrations", "error", err)
		os.Exit(1)
	}

	if cfg.SeedDefaultData {
		if err := database.SeedDefaults(db); err != nil {
			slog.Error("failed to seed defaults", "error", err)
			os.Exit(1)
		}
	}

	connectorRegistry, registryErr := connectordefaults.NewRegistry(cfg.YAMLConnectorsPath)
	if registryErr != nil {
		slog.Warn("connector registry loaded with warnings", "error", registryErr)
	}

	app := apihttp.NewServerWithRegistry(cfg, db, connectorRegistry)

	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	poller := scheduler.NewPoller(
		repository.NewTrackerRepository(db),
		connectorRegistry,
		scheduler.PollerConfig{
			Interval: time.Duration(cfg.PollingMinutes) * time.Minute,
		},
		slog.Default(),
	)
	if cfg.PollingEnabled {
		poller.Start(pollerCtx)
	}

	go func() {
		if err := app.Listen(":" + cfg.Port); err != nil {
			slog.Error("server stopped", "error", err)
		}
	}()

	slog.Info("api started", "port", cfg.Port, "env", cfg.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	slog.Info("shutting down server")
	pollerCancel()
	poller.StopWait(2 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}
