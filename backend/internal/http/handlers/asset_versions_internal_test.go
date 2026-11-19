package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A stamped asset URL is served as immutable, so the stamp has to change when
// the file does. It used to be read once and kept for the life of the
// process: a script edited under a running server kept its old URL, and with
// it the copy the browser already had.
func TestAssetStampFollowsTheFile(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "app.js")
	if err := os.WriteFile(script, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(script, saved, saved); err != nil {
		t.Fatal(err)
	}

	versions := newAssetVersions(dir)
	clock := saved.Add(time.Hour)
	versions.now = func() time.Time { return clock }

	first := versions.url("app.js")
	if !strings.HasPrefix(first, "/assets/app.js?v=") || len(first) <= len("/assets/app.js?v=") {
		t.Fatalf("stamped URL = %q", first)
	}

	// Saved again, within the same second as before: still a new URL.
	if err := os.WriteFile(script, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := saved.Add(300 * time.Millisecond)
	if err := os.Chtimes(script, edited, edited); err != nil {
		t.Fatal(err)
	}

	// The stamp is remembered for a moment, which spares a stat per asset on
	// every render...
	if got := versions.url("app.js"); got != first {
		t.Fatalf("within the stamp's life the URL changed: %q -> %q", first, got)
	}
	// ...and then read again.
	clock = clock.Add(assetStampLife)
	second := versions.url("app.js")
	if second == first {
		t.Fatalf("the URL is still %q after the file changed", second)
	}

	// A file that is not there has no stamp, and so revalidates like any
	// unstamped request; once it exists it gets one.
	if got := versions.url("later.css"); got != "/assets/later.css" {
		t.Fatalf("a missing file's URL = %q, want it unstamped", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "later.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(assetStampLife)
	if got := versions.url("later.css"); !strings.HasPrefix(got, "/assets/later.css?v=") {
		t.Fatalf("once the file exists its URL = %q, want it stamped", got)
	}
}
