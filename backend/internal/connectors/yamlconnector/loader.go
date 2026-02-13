package yamlconnector

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"gopkg.in/yaml.v3"
)

func LoadFromDir(dirPath string, client *http.Client) ([]connectors.Connector, error) {
	trimmed := strings.TrimSpace(dirPath)
	if trimmed == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read yaml connectors dir: %w", err)
	}

	files := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lower := strings.ToLower(entry.Name())
		if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			files = append(files, filepath.Join(trimmed, entry.Name()))
		}
	}
	sort.Strings(files)

	loaded := make([]connectors.Connector, 0, len(files))
	errors := make([]string, 0)

	for _, filePath := range files {
		content, err := os.ReadFile(filePath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", filepath.Base(filePath), err))
			continue
		}

		var cfg Config
		if err := yaml.Unmarshal(content, &cfg); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", filepath.Base(filePath), err))
			continue
		}
		if !cfg.isEnabled() {
			continue
		}

		connector, err := NewConnector(cfg, client)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", filepath.Base(filePath), err))
			continue
		}
		loaded = append(loaded, connector)
	}

	if len(errors) > 0 {
		return loaded, fmt.Errorf("yaml connectors failed to load: %s", strings.Join(errors, " | "))
	}

	return loaded, nil
}
