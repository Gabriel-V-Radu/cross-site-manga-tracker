package yamlconnector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDir(t *testing.T) {
	tmpDir := t.TempDir()

	valid := `
key: sitea
name: Site A
enabled: true
base_url: http://localhost:9999
search:
  path: /search
resolve:
  path: /resolve
`

	disabled := `
key: siteb
name: Site B
enabled: false
base_url: http://localhost:9999
search:
  path: /search
resolve:
  path: /resolve
`

	if err := os.WriteFile(filepath.Join(tmpDir, "a.yaml"), []byte(valid), 0o644); err != nil {
		t.Fatalf("write valid yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.yml"), []byte(disabled), 0o644); err != nil {
		t.Fatalf("write disabled yaml: %v", err)
	}

	loaded, err := LoadFromDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("load yaml dir: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded connector, got %d", len(loaded))
	}
	if loaded[0].Key() != "sitea" {
		t.Fatalf("expected sitea key, got %s", loaded[0].Key())
	}
}
