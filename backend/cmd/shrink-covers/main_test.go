package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/database"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.ApplyMigrations(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// grainyPicture is art-like enough to weigh something as a PNG and to be
// worth storing as a JPEG; the same picture on every run.
func grainyPicture(width int, height int) *image.RGBA {
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	state := uint32(88172645)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			noise := uint8(state) % 40
			picture.SetRGBA(x, y, color.RGBA{R: uint8(x*255/width) - noise/2, G: uint8(y*255/height) - noise/2, B: 90 + noise, A: 255})
		}
	}
	return picture
}

func writeCover(t *testing.T, dir string, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func encoded(t *testing.T, picture image.Image, asPNG bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var err error
	if asPNG {
		err = png.Encode(&buffer, picture)
	} else {
		err = jpeg.Encode(&buffer, picture, &jpeg.Options{Quality: 95})
	}
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buffer.Bytes()
}

func dirListing(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	listing := map[string]int64{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", entry.Name(), err)
		}
		listing[entry.Name()] = info.Size()
	}
	return listing
}

func cachedPath(t *testing.T, db *sql.DB, cacheKey string) string {
	t.Helper()
	var localPath string
	if err := db.QueryRow(`SELECT local_path FROM cover_cache WHERE cache_key = ?`, cacheKey).Scan(&localPath); err != nil {
		t.Fatalf("read cache entry %s: %v", cacheKey, err)
	}
	return localPath
}

// seedCovers puts four covers in a directory, with a cache entry each: an
// oversized PNG and an oversized JPEG, a cover that is light already, and a
// WebP the tool has no business with.
func seedCovers(t *testing.T, db *sql.DB) (dir string, sizes map[string]int64) {
	t.Helper()
	dir = t.TempDir()
	writeCover(t, dir, "aaa.png", encoded(t, grainyPicture(900, 1350), true))
	writeCover(t, dir, "bbb.jpg", encoded(t, grainyPicture(1280, 1820), false))
	writeCover(t, dir, "ccc.jpg", encoded(t, grainyPicture(300, 450), false))
	writeCover(t, dir, "ddd.webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "))
	writeCover(t, dir, "eee.png.tmp123", []byte("a download still being written"))

	for key, name := range map[string]string{"k-png": "aaa.png", "k-png-second-tracker": "aaa.png", "k-jpg": "bbb.jpg", "k-light": "ccc.jpg", "k-webp": "ddd.webp"} {
		if _, err := db.Exec(`
			INSERT INTO cover_cache (cache_key, cover_url, source_key, found, expires_at, local_path)
			VALUES (?, 'https://cdn.example/cover', 'mangadex', 1, '2036-01-01 00:00:00', ?)
		`, key, name); err != nil {
			t.Fatalf("seed cache entry: %v", err)
		}
	}
	return dir, dirListing(t, dir)
}

// Without -apply the tool only reports: no file is written or removed and no
// cache entry is changed.
func TestPreviewChangesNothing(t *testing.T) {
	db := openTestDB(t)
	dir, before := seedCovers(t, db)

	report, err := shrinkStoredCovers(context.Background(), dir, repository.NewCoverCacheRepository(db), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if report.Files != 3 || report.Shrunk != 2 || report.Renamed != 1 {
		t.Fatalf("report = %+v, want 3 decodable files, 2 oversized, 1 of them changing its name", report)
	}
	if report.BytesBefore != before["aaa.png"]+before["bbb.jpg"] || report.BytesAfter >= report.BytesBefore/2 {
		t.Fatalf("report sizes = %d -> %d, want the two oversized files' and a real saving", report.BytesBefore, report.BytesAfter)
	}

	after := dirListing(t, dir)
	if len(after) != len(before) {
		t.Fatalf("the preview changed the directory: %v -> %v", before, after)
	}
	for name, size := range before {
		if after[name] != size {
			t.Fatalf("the preview changed %s: %d -> %d bytes", name, size, after[name])
		}
	}
	if got := cachedPath(t, db, "k-png"); got != "aaa.png" {
		t.Fatalf("the preview changed a cache entry to %q", got)
	}
}

// With -apply each oversized cover becomes a smaller JPEG; one that changes
// its name takes every cache entry that named it along, and the old file
// goes. Everything else is left exactly as it was, and a second run finds
// nothing to do.
func TestApplyReplacesTheOversizedCovers(t *testing.T) {
	db := openTestDB(t)
	dir, before := seedCovers(t, db)
	store := repository.NewCoverCacheRepository(db)

	report, err := shrinkStoredCovers(context.Background(), dir, store, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.Shrunk != 2 || report.Renamed != 1 {
		t.Fatalf("report = %+v, want 2 covers shrunk, 1 renamed", report)
	}

	after := dirListing(t, dir)
	if _, still := after["aaa.png"]; still {
		t.Fatal("the oversized PNG is still there beside its smaller copy")
	}
	for _, name := range []string{"aaa.jpg", "bbb.jpg"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil || format != "jpeg" || config.Width != 640 {
			t.Fatalf("%s is a %dx%d %s (%v), want a JPEG 640 wide", name, config.Width, config.Height, format, err)
		}
	}
	if after["bbb.jpg"] >= before["bbb.jpg"]/2 {
		t.Fatalf("bbb.jpg went from %d to %d bytes", before["bbb.jpg"], after["bbb.jpg"])
	}
	for _, untouched := range []string{"ccc.jpg", "ddd.webp", "eee.png.tmp123"} {
		if after[untouched] != before[untouched] {
			t.Fatalf("%s was touched: %d -> %d bytes", untouched, before[untouched], after[untouched])
		}
	}
	if len(after) != len(before) {
		t.Fatalf("the directory holds %d files, want the same %d (no temporary file left behind): %v", len(after), len(before), after)
	}

	for key, want := range map[string]string{"k-png": "aaa.jpg", "k-png-second-tracker": "aaa.jpg", "k-jpg": "bbb.jpg", "k-light": "ccc.jpg", "k-webp": "ddd.webp"} {
		if got := cachedPath(t, db, key); got != want {
			t.Fatalf("cache entry %s names %q, want %q", key, got, want)
		}
	}

	again, err := shrinkStoredCovers(context.Background(), dir, store, true)
	if err != nil || again.Shrunk != 0 {
		t.Fatalf("a second run shrank %d covers (%v), want none", again.Shrunk, err)
	}
	for name, size := range after {
		if now := dirListing(t, dir)[name]; now != size {
			t.Fatalf("the second run changed %s: %d -> %d bytes", name, size, now)
		}
	}
}

// failingPaths refuses to move cache entries.
type failingPaths struct{}

func (failingPaths) RenameLocalPath(context.Context, string, string) (int64, error) {
	return 0, errors.New("database is locked")
}

// If the cache cannot be pointed at the new file, the old file stays: the
// entries still name it, and a cover must not be left with entries that name
// nothing. The run stops there and says so.
func TestApplyKeepsTheOldFileWhenTheCacheCannotBeUpdated(t *testing.T) {
	db := openTestDB(t)
	dir, _ := seedCovers(t, db)

	_, err := shrinkStoredCovers(context.Background(), dir, failingPaths{}, true)
	if err == nil {
		t.Fatal("the run reported success")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "aaa.png")); statErr != nil {
		t.Fatalf("the oversized cover was removed although its cache entries still name it: %v", statErr)
	}
	if got := cachedPath(t, db, "k-png"); got != "aaa.png" {
		t.Fatalf("the cache entry names %q", got)
	}
}

// A covers directory that does not exist yet is an empty one.
func TestMissingDirectoryIsNothingToDo(t *testing.T) {
	report, err := shrinkStoredCovers(context.Background(), filepath.Join(t.TempDir(), "no-covers-yet"), failingPaths{}, true)
	if err != nil || report.Files != 0 {
		t.Fatalf("report = %+v, %v", report, err)
	}
}
