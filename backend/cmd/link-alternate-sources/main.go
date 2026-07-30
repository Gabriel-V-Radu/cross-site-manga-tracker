// Command link-alternate-sources attaches an alternate source to trackers whose
// primary source is unreadable, so the poller's fallback has somewhere to go.
//
// It is driven by a reviewed CSV rather than by live searching, because matching
// series titles across sites produces false positives that only a human can
// settle (spin-offs, "(Colored)" reprints, doujinshi). Produce the CSV, review
// it, then feed it here.
//
// Required CSV columns: mangafire_url (the tracker's existing primary URL),
// mangabuddy_url (the alternate to attach) and action. Only rows whose action
// begins with "auto-link" are written; everything else is reported and skipped.
//
// Usage:
//
//	link-alternate-sources -db app.sqlite -csv matches.csv -source mangabuddy -dry-run
//	link-alternate-sources -db app.sqlite -csv matches.csv -source mangabuddy -apply
package main

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

// utf8BOM is the byte-order mark a spreadsheet export prepends to the first
// header cell. Spelled out in bytes so the source stays plain ASCII.
var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

type linkRow struct {
	primaryURL   string
	alternateURL string
	title        string
	action       string
}

func main() {
	dbPath := flag.String("db", "", "path to the SQLite database (required)")
	csvPath := flag.String("csv", "", "path to the reviewed CSV (required)")
	sourceKey := flag.String("source", "", "sources.key of the alternate source to attach (required)")
	primaryColumn := flag.String("primary-column", "mangafire_url", "CSV column holding the tracker's existing primary URL")
	alternateColumn := flag.String("alternate-column", "mangabuddy_url", "CSV column holding the alternate URL to attach")
	apply := flag.Bool("apply", false, "write the links; without this the run is a dry run")
	flag.Parse()

	if *dbPath == "" || *csvPath == "" || *sourceKey == "" {
		flag.Usage()
		log.Fatal("-db, -csv and -source are all required")
	}

	rows, err := readCSV(*csvPath, *primaryColumn, *alternateColumn)
	if err != nil {
		log.Fatalf("read csv: %v", err)
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	var sourceID int64
	if err := db.QueryRow(`SELECT id FROM sources WHERE key = ?`, *sourceKey).Scan(&sourceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Fatalf("source %q is not registered; run migrations first", *sourceKey)
		}
		log.Fatalf("look up source %q: %v", *sourceKey, err)
	}

	if !*apply {
		fmt.Println("DRY RUN — nothing will be written. Re-run with -apply to commit.")
	}
	fmt.Printf("source %q = id %d\n\n", *sourceKey, sourceID)

	var linked, alreadyLinked, skipped, unmatched int

	for _, row := range rows {
		if !strings.HasPrefix(row.action, "auto-link") {
			skipped++
			continue
		}
		if row.alternateURL == "" {
			skipped++
			continue
		}

		// One primary URL can back several trackers (the same series tracked on
		// more than one profile), so every match is linked.
		trackerIDs, err := trackersBySourceURL(db, row.primaryURL)
		if err != nil {
			log.Fatalf("look up trackers for %s: %v", row.primaryURL, err)
		}
		if len(trackerIDs) == 0 {
			fmt.Printf("  NO TRACKER  %-50s %s\n", trunc(row.title, 50), row.primaryURL)
			unmatched++
			continue
		}

		for _, trackerID := range trackerIDs {
			exists, err := linkExists(db, trackerID, sourceID, row.alternateURL)
			if err != nil {
				log.Fatalf("check existing link for tracker %d: %v", trackerID, err)
			}
			if exists {
				alreadyLinked++
				continue
			}

			if *apply {
				if err := insertLink(db, trackerID, sourceID, row.alternateURL); err != nil {
					log.Fatalf("link tracker %d: %v", trackerID, err)
				}
			}
			fmt.Printf("  %-8s tracker %-5d %-45s -> %s\n",
				verb(*apply), trackerID, trunc(row.title, 45), row.alternateURL)
			linked++
		}
	}

	fmt.Printf("\n%s: %d link(s) for %d CSV row(s)\n", summaryLabel(*apply), linked, len(rows))
	fmt.Printf("already linked: %d\nskipped (not auto-link): %d\nno matching tracker: %d\n",
		alreadyLinked, skipped, unmatched)
	if !*apply && linked > 0 {
		fmt.Println("\nNothing was written. Re-run with -apply to commit these links.")
	}
}

func verb(apply bool) string {
	if apply {
		return "LINKED"
	}
	return "WOULD"
}

func summaryLabel(apply bool) string {
	if apply {
		return "written"
	}
	return "would write"
}

func readCSV(path, primaryColumn, alternateColumn string) ([]linkRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// A spreadsheet export prepends a BOM, which csv.Reader would otherwise treat
	// as part of the first header cell and reject as a bare quote.
	buffered := bufio.NewReader(file)
	if prefix, err := buffered.Peek(len(utf8BOM)); err == nil && string(prefix) == utf8BOM {
		_, _ = buffered.Discard(len(utf8BOM))
	}

	reader := csv.NewReader(buffered)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	index := map[string]int{}
	for position, name := range header {
		// Strip a UTF-8 BOM so a spreadsheet-exported CSV still resolves columns.
		index[strings.TrimSpace(strings.TrimPrefix(name, utf8BOM))] = position
	}
	for _, required := range []string{primaryColumn, alternateColumn, "action"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("csv is missing required column %q", required)
		}
	}
	titleColumn, hasTitle := index["mangafire_title"]

	var rows []linkRow
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		get := func(position int) string {
			if position < 0 || position >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[position])
		}

		row := linkRow{
			primaryURL:   get(index[primaryColumn]),
			alternateURL: get(index[alternateColumn]),
			action:       get(index["action"]),
		}
		if hasTitle {
			row.title = get(titleColumn)
		}
		if row.primaryURL == "" {
			continue
		}
		rows = append(rows, row)
	}

	return rows, nil
}

func trackersBySourceURL(db *sql.DB, sourceURL string) ([]int64, error) {
	rows, err := db.Query(`
		SELECT id FROM trackers
		WHERE LOWER(TRIM(source_url)) = LOWER(TRIM(?))
		ORDER BY id ASC
	`, sourceURL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func linkExists(db *sql.DB, trackerID, sourceID int64, sourceURL string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(1) FROM tracker_sources
		WHERE tracker_id = ? AND source_id = ? AND LOWER(TRIM(source_url)) = LOWER(TRIM(?))
	`, trackerID, sourceID, sourceURL).Scan(&count)
	return count > 0, err
}

func insertLink(db *sql.DB, trackerID, sourceID int64, sourceURL string) error {
	_, err := db.Exec(`
		INSERT INTO tracker_sources (tracker_id, source_id, source_item_id, source_url)
		VALUES (?, ?, NULL, ?)
		ON CONFLICT(tracker_id, source_id, source_url)
		DO UPDATE SET updated_at = CURRENT_TIMESTAMP
	`, trackerID, sourceID, strings.TrimSpace(sourceURL))
	return err
}

func trunc(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max-1] + "…"
}
