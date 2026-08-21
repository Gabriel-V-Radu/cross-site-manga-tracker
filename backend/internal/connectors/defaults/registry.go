package defaults

import (
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/asuracomic"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/comick"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/flamecomics"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/freewebnovel"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangabuddy"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangadex"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangafire"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mangaupdates"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/mgeko"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/webtoons"
	"github.com/gabriel/cross-site-tracker/backend/internal/connectors/native/weebcentral"
)

func NewRegistry() *connectors.Registry {
	registry := connectors.NewRegistry()
	_ = registry.Register(mangadex.NewConnector())
	_ = registry.Register(mangafire.NewConnector())
	_ = registry.Register(mangabuddy.NewConnector())
	_ = registry.Register(asuracomic.NewConnector())
	_ = registry.Register(flamecomics.NewConnector())
	_ = registry.Register(mgeko.NewConnector())
	_ = registry.Register(webtoons.NewConnector())
	_ = registry.Register(freewebnovel.NewConnector())
	_ = registry.Register(weebcentral.NewConnector())
	_ = registry.Register(mangaupdates.NewConnector())
	_ = registry.Register(comick.NewConnector())

	return registry
}
