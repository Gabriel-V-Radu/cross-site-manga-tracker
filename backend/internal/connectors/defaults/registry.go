package defaults

import (
	"github.com/gabriel/cross-site-tracker/backend/internal/config"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/asuracomic"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/flamecomics"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangadex"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangafire"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mgeko"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/webtoons"
)

func NewRegistry(cfg config.Config) *connectors.Registry {
	registry := connectors.NewRegistry()

	mf := mangafire.NewConnector()
	if cfg.CFBrowserURL != "" {
		mf.WithCFBrowserURL(cfg.CFBrowserURL)
	}

	_ = registry.Register(mangadex.NewConnector())
	_ = registry.Register(mf)
	_ = registry.Register(asuracomic.NewConnector())
	_ = registry.Register(flamecomics.NewConnector())
	_ = registry.Register(mgeko.NewConnector())
	_ = registry.Register(webtoons.NewConnector())

	return registry
}
