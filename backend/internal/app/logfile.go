package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// The server's log goes to stdout, where Docker keeps it — with the
// container. Every deploy recreates the container, so every deploy threw the
// log away, and with it the only record of what the poller had been finding
// and failing at. The host's journal is no better a home on the Pi: Raspberry
// Pi OS keeps it in memory (Storage=volatile), so there it would survive a
// deploy and be lost on a reboot instead.
//
// So the server also appends its log to a file in the data volume, which
// outlives both. The file is capped: once it passes logFileMaxBytes it is
// rotated, and logFileKeep rotated files are kept. At the default level the
// server writes some tens of kilobytes a day, which makes that about a year of
// history in at most 20 MB; at DEBUG it writes thousands of lines per poll
// cycle and the cap is what keeps the card from filling.
const (
	logFileMaxBytes = 5 << 20
	logFileKeep     = 3
)

// rotatingLogFile is an append-only log file that rotates by size: path
// becomes path.1, path.1 becomes path.2, and so on up to keep, the oldest
// dropping off. It is safe for concurrent use, and a write never fails the
// caller: logging must not be what stops the server, so a write that cannot
// be made is reported once on stderr and dropped.
type rotatingLogFile struct {
	path     string
	maxBytes int64
	keep     int

	mu       sync.Mutex
	file     *os.File
	size     int64
	reported bool
	// closed stops a write that arrives after Close — the default logger
	// outlives the runtime that opened the file — from reopening it.
	closed bool
}

func openRotatingLogFile(path string, maxBytes int64, keep int) (*rotatingLogFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	log := &rotatingLogFile{path: path, maxBytes: maxBytes, keep: keep}
	if err := log.open(); err != nil {
		return nil, err
	}
	return log, nil
}

// open (re)opens the current file for appending. Callers hold l.mu, or own l
// exclusively.
func (l *rotatingLogFile) open() error {
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	l.file, l.size = file, info.Size()
	return nil
}

// Write appends p, rotating first when p would take the file past its cap. A
// record is never split across two files: slog hands over one whole line per
// call.
func (l *rotatingLogFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return len(p), nil
	}
	if l.file != nil && l.size > 0 && l.size+int64(len(p)) > l.maxBytes {
		if err := l.rotate(); err != nil {
			l.report(err)
		}
	}
	if l.file == nil {
		// A rotation that could not reopen the file left none; try again
		// rather than staying silent for the rest of the process.
		if err := l.open(); err != nil {
			l.report(err)
			return len(p), nil
		}
	}
	written, err := l.file.Write(p)
	l.size += int64(written)
	if err != nil {
		l.report(err)
	}
	return len(p), nil
}

// rotate shifts the kept files up by one and starts a fresh current file.
// Callers hold l.mu.
func (l *rotatingLogFile) rotate() error {
	_ = l.file.Close()
	l.file = nil
	if l.keep <= 0 {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("drop full log file: %w", err)
		}
		return l.open()
	}
	// Oldest first, so each rename lands on a name that was just vacated;
	// os.Rename replaces the destination, which is how the oldest is dropped.
	for index := l.keep - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", l.path, index)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", l.path, index+1)); err != nil {
			return fmt.Errorf("rotate log file: %w", err)
		}
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return fmt.Errorf("rotate log file: %w", err)
	}
	return l.open()
}

// report says once, on stderr, that the log file is not being written. Callers
// hold l.mu.
func (l *rotatingLogFile) report(err error) {
	if l.reported {
		return
	}
	l.reported = true
	fmt.Fprintf(os.Stderr, "log file %s: %v (further failures are not reported)\n", l.path, err)
}

// Close closes the file for good. Safe on a nil receiver, which is what a
// runtime that logs to stdout only holds.
func (l *rotatingLogFile) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
