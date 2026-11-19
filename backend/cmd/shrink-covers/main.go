// Command shrink-covers goes over the covers already stored on disk and
// replaces the oversized ones with the smaller copy the server now stores for
// a new cover (resolve.ShrinkCover): a JPEG no wider than a card needs. The
// server only shrinks covers as it downloads them; the ones downloaded before
// that — single files of five to seven megabytes among them — stay as they
// are until this is run.
//
// Without -apply it is a preview: it reads the files, says what it would do
// and how much that would save, and writes nothing, neither to the directory
// nor to the database.
//
// With -apply, each oversized cover is written as a new file and the old one
// removed. A cover that was not a JPEG changes its name with its format, and
// the entries of the cover cache that named the old file are pointed at the
// new one. The server reads those names once, when it starts, so RUN IT WITH
// THE SERVER STOPPED: a running server would go on handing out the names of
// files that are no longer there until its next restart.
//
// It is safe to run twice, and to interrupt: a cover already shrunk is left
// alone, and each file is finished — new file, cache entries, old file, in
// that order — before the next is touched.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/app"
	"github.com/gabriel/cross-site-tracker/backend/internal/repository"
	"github.com/gabriel/cross-site-tracker/backend/internal/resolve"
)

func main() {
	var apply bool
	flag.BoolVar(&apply, "apply", false, "Replace the oversized covers. Without this flag, the command is a dry-run preview.")
	flag.Parse()

	// Migrations only when the run is going to write, like the other tools: a
	// preview must leave the database it is pointed at as it found it.
	level := slog.LevelInfo
	runtime, err := app.Open(app.Options{Migrate: apply, LogLevel: &level})
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer runtime.Close()

	report, err := shrinkStoredCovers(context.Background(), runtime.Config.CoversDir(), repository.NewCoverCacheRepository(runtime.DB), apply)
	if err != nil {
		slog.Error("shrink aborted", "error", err, "shrunk_so_far", report.Shrunk)
		runtime.Close()
		os.Exit(1)
	}
	slog.Info("covers checked",
		"dry_run", !apply,
		"files", report.Files,
		"oversized", report.Shrunk,
		"renamed", report.Renamed,
		"before", megabytes(report.BytesBefore),
		"after", megabytes(report.BytesAfter),
		"saved", megabytes(report.BytesBefore-report.BytesAfter))
	if !apply && report.Shrunk > 0 {
		slog.Info("nothing was written; stop the server and run again with -apply to replace them")
	}
}

func megabytes(size int64) string {
	return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
}

// coverPaths is the part of the cover cache the tool writes to.
type coverPaths interface {
	RenameLocalPath(ctx context.Context, oldName string, newName string) (int64, error)
}

// shrinkReport is what a run found and did. BytesBefore and BytesAfter cover
// the oversized files only.
type shrinkReport struct {
	Files       int
	Shrunk      int
	Renamed     int
	BytesBefore int64
	BytesAfter  int64
}

// maxStoredCoverBytes is the largest file the tool reads into memory. The
// server never stores a cover over eight megabytes; anything bigger is not
// one of its files.
const maxStoredCoverBytes = 32 << 20

// shrinkStoredCovers checks every cover in dir and, when apply is set,
// replaces the oversized ones. The report counts what would be done on a
// preview, and what was done otherwise.
func shrinkStoredCovers(ctx context.Context, dir string, paths coverPaths, apply bool) (shrinkReport, error) {
	var report shrinkReport

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return report, nil
	}
	if err != nil {
		return report, fmt.Errorf("read the covers directory: %w", err)
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		// Only what the standard library decodes; the rest ShrinkCover would
		// turn down anyway, after the file had been read. A download still
		// being written carries ".tmp" in its name and ends in none of these.
		if !entry.Type().IsRegular() || (ext != ".jpg" && ext != ".jpeg" && ext != ".png") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxStoredCoverBytes {
			continue
		}
		report.Files++

		original, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return report, fmt.Errorf("read %s: %w", name, err)
		}
		smaller, smallerExt, shrunk := resolve.ShrinkCover(original)
		if !shrunk {
			continue
		}
		newName := strings.TrimSuffix(name, filepath.Ext(name)) + smallerExt

		slog.Info("oversized cover", "file", name, "stored_as", newName, "from_bytes", len(original), "to_bytes", len(smaller))
		if apply {
			renamed, err := replaceCover(ctx, dir, paths, name, newName, smaller)
			if err != nil {
				return report, err
			}
			if renamed {
				report.Renamed++
			}
		} else if newName != name {
			report.Renamed++
		}
		report.Shrunk++
		report.BytesBefore += int64(len(original))
		report.BytesAfter += int64(len(smaller))
	}
	return report, nil
}

// replaceCover puts the smaller copy in place: the new file first, complete
// before it takes its name; then, when the name changed, the cache entries
// that named the old file; and only then the old file. Stopped at any point
// it leaves a cover the server can serve, and a later run finishes the job —
// what it finds then is at worst an old file nothing refers to any more.
// It reports whether the cover changed its name.
func replaceCover(ctx context.Context, dir string, paths coverPaths, name string, newName string, smaller []byte) (bool, error) {
	temp, err := os.CreateTemp(dir, newName+".tmp*")
	if err != nil {
		return false, fmt.Errorf("create the smaller copy of %s: %w", name, err)
	}
	tempName := temp.Name()
	_, writeErr := temp.Write(smaller)
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tempName)
		return false, fmt.Errorf("write the smaller copy of %s: write=%v close=%v", name, writeErr, closeErr)
	}
	if err := os.Rename(tempName, filepath.Join(dir, newName)); err != nil {
		_ = os.Remove(tempName)
		return false, fmt.Errorf("put the smaller copy of %s in place: %w", name, err)
	}
	if newName == name {
		return false, nil
	}

	if _, err := paths.RenameLocalPath(ctx, name, newName); err != nil {
		return false, fmt.Errorf("point the cover cache from %s at %s: %w", name, newName, err)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove the oversized %s: %w", name, err)
	}
	return true, nil
}
