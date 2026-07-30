package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp csv: %v", err)
	}
	return path
}

func TestReadCSVSkipsLeadingBOM(t *testing.T) {
	// A spreadsheet export starts with a BOM and quotes every field; the reader
	// must still resolve the header columns.
	content := utf8BOM + `"mangafire_title","mangafire_url","mangabuddy_url","action"
"A","https://mangafire.to/title/a","https://mangabuddy1.co.uk/series/a.ZD1","auto-link"
`
	rows, err := readCSV(writeTemp(t, "bom.csv", content), "mangafire_url", "mangabuddy_url")
	if err != nil {
		t.Fatalf("read csv failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].primaryURL != "https://mangafire.to/title/a" {
		t.Fatalf("unexpected primary url %q", rows[0].primaryURL)
	}
	if rows[0].alternateURL != "https://mangabuddy1.co.uk/series/a.ZD1" {
		t.Fatalf("unexpected alternate url %q", rows[0].alternateURL)
	}
	if rows[0].title != "A" {
		t.Fatalf("expected the title column to be picked up, got %q", rows[0].title)
	}
}

func TestReadCSVWithoutBOM(t *testing.T) {
	content := `mangafire_url,mangabuddy_url,action
https://mangafire.to/title/b,https://mangabuddy1.co.uk/series/b.ZD2,auto-link (confirmado)
`
	rows, err := readCSV(writeTemp(t, "plain.csv", content), "mangafire_url", "mangabuddy_url")
	if err != nil {
		t.Fatalf("read csv failed: %v", err)
	}
	if len(rows) != 1 || rows[0].action != "auto-link (confirmado)" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestReadCSVDropsRowsWithoutPrimaryURL(t *testing.T) {
	content := `mangafire_url,mangabuddy_url,action
,https://mangabuddy1.co.uk/series/c.ZD3,auto-link
https://mangafire.to/title/d,https://mangabuddy1.co.uk/series/d.ZD4,auto-link
`
	rows, err := readCSV(writeTemp(t, "missing.csv", content), "mangafire_url", "mangabuddy_url")
	if err != nil {
		t.Fatalf("read csv failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the row without a primary url to be dropped, got %d rows", len(rows))
	}
}

func TestReadCSVRequiresExpectedColumns(t *testing.T) {
	content := `something_else,action
x,auto-link
`
	if _, err := readCSV(writeTemp(t, "bad.csv", content), "mangafire_url", "mangabuddy_url"); err == nil {
		t.Fatalf("expected an error when a required column is missing")
	}
}

// TestReadCSVPreservesActionForFiltering documents that readCSV does not filter:
// the caller decides what "auto-link" means, so review rows still come through
// and get reported rather than silently vanishing.
func TestReadCSVPreservesActionForFiltering(t *testing.T) {
	content := `mangafire_url,mangabuddy_url,action
https://mangafire.to/title/e,https://mangabuddy1.co.uk/series/e.ZD5,manual review
https://mangafire.to/title/f,,no equivalent found
`
	rows, err := readCSV(writeTemp(t, "review.csv", content), "mangafire_url", "mangabuddy_url")
	if err != nil {
		t.Fatalf("read csv failed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both non-auto-link rows to be returned, got %d", len(rows))
	}
	if rows[0].action != "manual review" || rows[1].action != "no equivalent found" {
		t.Fatalf("actions not preserved: %#v", rows)
	}
}
