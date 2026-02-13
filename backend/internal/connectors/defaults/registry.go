package defaults

import (
	"fmt"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangadex"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangaplus"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/yamlconnector"
)

func NewRegistry(yamlConnectorsPath string) (*connectors.Registry, error) {
	registry := connectors.NewRegistry()
	_ = registry.Register(mangadex.NewConnector())
	_ = registry.Register(mangaplus.NewConnector())

	loaded, loadErr := yamlconnector.LoadFromDir(yamlConnectorsPath, nil)
	for _, connector := range loaded {
		if err := registry.Register(connector); err != nil {
			if loadErr == nil {
				loadErr = fmt.Errorf("register yaml connector %q: %w", connector.Key(), err)
			}
		}
	}

	return registry, loadErr
}
