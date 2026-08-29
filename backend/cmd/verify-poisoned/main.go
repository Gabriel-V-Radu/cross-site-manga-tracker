// Command verify-poisoned is a throwaway checker: for the given tracker ids it
// resolves every linked source live (with the outlier-fixed connectors) and
// prints each answer, so the repair values can be chosen from evidence.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
	"github.com/gabriel/cross-site-tracker/backend/internal/database"
)

func main() {
	dbPath := flag.String("db", "", "Path to a snapshot of app.sqlite")
	idsRaw := flag.String("ids", "", "Comma-separated tracker ids")
	flag.Parse()

	ids := map[int64]bool{}
	for _, part := range strings.Split(*idsRaw, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			ids[id] = true
		}
	}
	if *dbPath == "" || len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "-db and -ids are required")
		os.Exit(1)
	}

	db, err := database.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	registry := connectordefaults.NewRegistry()

	type link struct{ Key, URL string }
	type tracker struct {
		ID     int64
		Title  string
		Stored sql.NullFloat64
		Read   sql.NullFloat64
		Links  []link
	}
	trackers := map[int64]*tracker{}

	rows, err := db.Query(`
		SELECT t.id, t.title, t.latest_known_chapter, t.last_read_chapter, s.key, t.source_url
		FROM trackers t INNER JOIN sources s ON s.id = t.source_id
	`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for rows.Next() {
		t := &tracker{}
		var key, url string
		if err := rows.Scan(&t.ID, &t.Title, &t.Stored, &t.Read, &key, &url); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !ids[t.ID] {
			continue
		}
		t.Links = append(t.Links, link{key, url})
		trackers[t.ID] = t
	}
	// Without this an error mid-stream ends the loop exactly like a clean EOF,
	// and the tool prints a partial link set — the worst possible outcome for
	// a command whose whole job is to be the evidence behind a repair.
	if err := rows.Err(); err != nil {
		rows.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rows.Close()

	altRows, err := db.Query(`
		SELECT ts.tracker_id, s.key, ts.source_url
		FROM tracker_sources ts INNER JOIN sources s ON s.id = ts.source_id
	`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for altRows.Next() {
		var id int64
		var key, url string
		if err := altRows.Scan(&id, &key, &url); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if t, ok := trackers[id]; ok {
			t.Links = append(t.Links, link{key, url})
		}
	}
	if err := altRows.Err(); err != nil {
		altRows.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	altRows.Close()

	ordered := make([]*tracker, 0, len(trackers))
	for _, t := range trackers {
		ordered = append(ordered, t)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	for _, t := range ordered {
		fmt.Printf("\n#%d %q stored=%v read=%v\n", t.ID, t.Title, nullable(t.Stored), nullable(t.Read))
		seen := map[string]bool{}
		for _, l := range t.Links {
			dedupe := l.Key + "|" + l.URL
			if seen[dedupe] || strings.TrimSpace(l.URL) == "" {
				continue
			}
			seen[dedupe] = true
			conn, ok := registry.Get(l.Key)
			if !ok {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			result, err := conn.ResolveByURL(ctx, l.URL)
			cancel()
			switch {
			case err != nil:
				fmt.Printf("  %-14s ERR %v (%s)\n", l.Key, err, l.URL)
			case result == nil || result.LatestChapter == nil:
				fmt.Printf("  %-14s no chapter (%s)\n", l.Key, l.URL)
			default:
				fmt.Printf("  %-14s %.1f (%s)\n", l.Key, *result.LatestChapter, l.URL)
			}
		}
	}
}

func nullable(v sql.NullFloat64) string {
	if !v.Valid {
		return "-"
	}
	return strconv.FormatFloat(v.Float64, 'f', -1, 64)
}
