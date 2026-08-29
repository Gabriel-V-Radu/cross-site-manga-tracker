package config

import (
	"path/filepath"
	"testing"
)

// TestLoadDefaultPaths pins the deployed layout. The container copies web/ next
// to the binary and mounts the data volume beside it, so these defaults are
// what production runs on: changing them silently would serve no assets and
// write covers where nothing serves them from.
func TestLoadDefaultPaths(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"web dir", cfg.WebDir, "./web"},
		{"data dir", cfg.DataDir, "./data"},
		{"assets", cfg.AssetsDir(), filepath.Join("web", "assets")},
		{"templates", cfg.TemplatesGlob(), filepath.Join("web", "templates", "*.html")},
		{"covers", cfg.CoversDir(), filepath.Join("data", "covers")},
		{"uploads", cfg.UploadsDir(), filepath.Join("data", "uploads")},
		{"site logos", cfg.SourceLogosDir(), filepath.Join("data", "uploads", "site-logos")},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s = %q, want %q", testCase.name, testCase.got, testCase.want)
		}
	}
}

func TestLoadOverridesPaths(t *testing.T) {
	t.Setenv("WEB_DIR", "/srv/web")
	t.Setenv("DATA_DIR", "/mnt/tracker")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got, want := cfg.CoversDir(), filepath.Join("/mnt/tracker", "covers"); got != want {
		t.Errorf("covers dir = %q, want %q", got, want)
	}
	if got, want := cfg.TemplatesGlob(), filepath.Join("/srv/web", "templates", "*.html"); got != want {
		t.Errorf("templates glob = %q, want %q", got, want)
	}
}

// TestLoadKeepsRunningOnMalformedValues: a typo must not stop the process from
// starting, and must not be mistaken for the value having been applied — the
// warning is what makes the difference, and it goes through slog rather than a
// return value.
func TestLoadKeepsRunningOnMalformedValues(t *testing.T) {
	t.Setenv("POLLING_MINUTES", "3O")
	t.Setenv("POLLING_ENABLED", "yes please")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.PollingMinutes != 30 {
		t.Errorf("polling minutes = %d, want the 30 default", cfg.PollingMinutes)
	}
	if !cfg.PollingEnabled {
		t.Errorf("polling enabled = false, want the true default")
	}
}
