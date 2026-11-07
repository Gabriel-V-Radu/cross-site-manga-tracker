// Command poll-once runs one full poll cycle against a database snapshot,
// with the real connectors and real network traffic. It exists to rehearse
// poller changes against production data before they reach the Pi: point it
// (built with -race) at a copy of the Pi's app.sqlite and watch cycle time,
// per-shard durations, and refusals. The snapshot IS written to — poll
// results persist into it — so never aim it at a live database file.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/scheduler"
)

func main() {
	dbPath := flag.String("db", "", "Path to a snapshot copy of app.sqlite (it will be written to)")
	// The poller rotates each tracker across up to three linked sites, so it
	// takes three cycles for every site to have had its turn once.
	cycles := flag.Int("cycles", 1, "How many poll cycles to run back to back")
	flag.Parse()

	if *dbPath == "" || *cycles < 1 {
		fmt.Fprintln(os.Stderr, "-db is required and -cycles must be at least 1")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	db, err := database.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	// The snapshot is usually copied from a live database mid-write, WAL and
	// all; refuse to rehearse against a torn copy.
	var integrity string
	if err := db.QueryRow(`PRAGMA quick_check;`).Scan(&integrity); err != nil || integrity != "ok" {
		fmt.Fprintf(os.Stderr, "integrity check failed: %q %v\n", integrity, err)
		os.Exit(1)
	}

	// The binary being rehearsed may carry a migration the snapshot has not
	// seen yet; the server applies them on start, and so does the rehearsal.
	if err := database.ApplyMigrations(db); err != nil {
		fmt.Fprintln(os.Stderr, "apply migrations:", err)
		os.Exit(1)
	}

	repo := repository.NewTrackerRepository(db)
	registry := connectordefaults.NewRegistry()
	poller := scheduler.NewPoller(repo, registry, scheduler.PollerConfig{}, logger)

	trackers, err := repo.ListForPolling(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "list trackers:", err)
		os.Exit(1)
	}
	// By primary source, for comparison: the cycle itself shards by the site
	// each poll's turn goes to, and logs those shards as it finishes them.
	primaries := map[string]int{}
	for _, tracker := range trackers {
		primaries[tracker.SourceKey]++
	}
	logger.Info("starting poll cycles", "trackers", len(trackers), "cycles", *cycles)
	for key, count := range primaries {
		logger.Info("trackers by primary source", "sourceKey", key, "trackers", count)
	}

	for cycle := 1; cycle <= *cycles; cycle++ {
		started := time.Now()
		if err := poller.RunOnce(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "poll cycle failed:", err)
			os.Exit(1)
		}
		logger.Info("poll cycle finished", "cycle", cycle, "took", time.Since(started).Round(time.Second).String())

		// The same verdicts the dashboard's source health panel would show
		// after this cycle, so a rehearsal also says which sites the change
		// left failing.
		report := registry.SourceHealthReport(time.Now())
		for _, source := range report.Sources {
			logger.Info("source health",
				"cycle", cycle,
				"sourceKey", source.Key,
				"state", source.State,
				"reason", source.Reason,
				"answered", source.Answered,
				"failed", source.Failed,
				"noChapter", source.NoChapter,
				"refused", source.Refused,
				"covered", source.Covered,
				"unchecked", source.UncheckedCount,
				"lastError", source.LastError)
		}
		for _, orphan := range report.Orphans {
			logger.Warn("trackers on a source with no connector", "sourceKey", orphan.Key, "count", orphan.Count)
		}
	}
}
