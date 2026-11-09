package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// The file is capped: a line that would take it past the cap starts a new
// file, the old ones shift up, and only `keep` of them stay.
func TestRotatingLogFileRotatesBySizeAndKeepsAFewFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	log, err := openRotatingLogFile(path, 20, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer log.Close()

	for _, line := range []string{"first line 0001\n", "second line 002\n", "third line 0003\n", "fourth line 004\n"} {
		if written, err := log.Write([]byte(line)); err != nil || written != len(line) {
			t.Fatalf("write %q: %d, %v", line, written, err)
		}
	}

	if got := readFile(t, path); got != "fourth line 004\n" {
		t.Fatalf("current file = %q, want only the newest line", got)
	}
	if got := readFile(t, path+".1"); got != "third line 0003\n" {
		t.Fatalf("app.log.1 = %q, want the line before", got)
	}
	if got := readFile(t, path+".2"); got != "second line 002\n" {
		t.Fatalf("app.log.2 = %q", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("only two rotated files are kept, app.log.3 exists (%v)", err)
	}
}

// A restart appends to the file it finds, and a write after Close is dropped
// rather than reopening it.
func TestRotatingLogFileAppendsAcrossRestartsAndStaysClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	first, err := openRotatingLogFile(path, 1<<20, 3)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = first.Write([]byte("before the restart\n"))
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, _ = first.Write([]byte("after close\n"))

	second, err := openRotatingLogFile(path, 1<<20, 3)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_, _ = second.Write([]byte("after the restart\n"))
	_ = second.Close()

	if got := readFile(t, path); got != "before the restart\nafter the restart\n" {
		t.Fatalf("file = %q", got)
	}

	var none *rotatingLogFile
	if err := none.Close(); err != nil {
		t.Fatalf("closing no log file must be a no-op, got %v", err)
	}
}

// The server's log lands in LOG_FILE as well as on stdout; a tool pointed at
// the same environment leaves the server's history alone.
func TestOpenAppendsTheServersLogToTheConfiguredFile(t *testing.T) {
	pointAtTempDatabase(t)
	logPath := filepath.Join(t.TempDir(), "logs", "app.log")
	t.Setenv("LOG_FILE", logPath)
	previous := slog.Default()
	defer slog.SetDefault(previous)

	tool, err := Open(Options{})
	if err != nil {
		t.Fatalf("open as a tool: %v", err)
	}
	slog.Info("a tool's line")
	tool.Close()
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("a tool must not write the server's log file (%v)", err)
	}

	server, err := Open(Options{Migrate: true, JSONLogs: true, FileLog: true})
	if err != nil {
		t.Fatalf("open as the server: %v", err)
	}
	slog.Info("poll found a new chapter", "via", "feed:mangahub")
	server.Close()

	logged := readFile(t, logPath)
	if !strings.Contains(logged, `"msg":"poll found a new chapter"`) || !strings.Contains(logged, `"via":"feed:mangahub"`) {
		t.Fatalf("the server's line is not in the log file: %q", logged)
	}
}

// A log file that cannot be opened must not stop the server from starting.
func TestOpenStartsWithoutALogFileItCannotOpen(t *testing.T) {
	pointAtTempDatabase(t)
	// A directory where the file should be.
	blocked := filepath.Join(t.TempDir(), "app.log")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("LOG_FILE", blocked)
	previous := slog.Default()
	defer slog.SetDefault(previous)

	server, err := Open(Options{JSONLogs: true, FileLog: true})
	if err != nil {
		t.Fatalf("a bad LOG_FILE must not fail startup: %v", err)
	}
	server.Close()
}
