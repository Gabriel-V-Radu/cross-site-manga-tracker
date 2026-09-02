// Package app is the one startup path the binaries share: load the
// configuration, install the logger, open the database, and — when the caller
// is going to write — bring the schema up to date. The api binary and the two
// operational tools used to each carry their own copy of this sequence, and
// the copies disagreed on the one thing that matters: whether a preview run
// migrates the live database. A dry run from a newer checkout than the running
// image must not advance the schema under it, so migration is opt-in here and
// each binary states when it wants it.
package app

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
)

// Options say what a binary needs of the shared startup.
type Options struct {
	// Migrate applies pending migrations and, when the configuration asks for
	// it, seeds the default rows. Off for previews.
	Migrate bool
	// JSONLogs selects the structured handler the long-running server logs
	// with; off gives the text handler the tools print for a human at a
	// terminal.
	JSONLogs bool
	// LogLevel overrides the configured level when non-nil. The tools run at
	// Info regardless of what the server's environment says.
	LogLevel *slog.Level
}

// Runtime is what Open hands back: the loaded configuration and an open
// database the caller owns and must Close.
type Runtime struct {
	Config config.Config
	DB     *sql.DB
}

// Open runs the startup sequence. Every failure is returned rather than
// logged-and-exited, so the binary decides how to report it; the logger is
// installed as the default before anything that might log runs.
func Open(opts Options) (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	level := cfg.LogLevel
	if opts.LogLevel != nil {
		level = *opts.LogLevel
	}
	handlerOptions := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if opts.JSONLogs {
		handler = slog.NewJSONHandler(os.Stdout, handlerOptions)
	} else {
		handler = slog.NewTextHandler(os.Stdout, handlerOptions)
	}
	slog.SetDefault(slog.New(handler))

	db, err := database.Open(cfg.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", cfg.SQLitePath, err)
	}

	if opts.Migrate {
		if err := database.ApplyMigrations(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply migrations: %w", err)
		}
		if cfg.SeedDefaultData {
			if err := database.SeedDefaults(db); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("seed defaults: %w", err)
			}
		}
	}

	return &Runtime{Config: cfg, DB: db}, nil
}

// Close releases the database. Safe on a nil runtime, so a caller can defer it
// right after Open without checking the error first.
func (r *Runtime) Close() {
	if r == nil || r.DB == nil {
		return
	}
	_ = r.DB.Close()
}
