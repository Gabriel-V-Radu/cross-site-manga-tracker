package defaults_test

import (
	"strings"
	"testing"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
	connectordefaults "github.com/gabriel/cross-site-tracker/backend/internal/connectors/defaults"
)

// hostKeyMappings is the host to connector-key table the registry used to carry
// as a hand-maintained switch. That knowledge now lives in each connector's
// SiteInfo.Hosts, and this test is what keeps the move honest: every host here
// belongs to trackers already stored in the database, so a connector that drops
// a domain — especially a historical one it was renamed away from — silently
// breaks chapter resolution for every tracker linked before the rename.
var hostKeyMappings = map[string]string{
	"mangadex.org":         "mangadex",
	"mangafire.to":         "mangafire",
	"asuracomic.net":       "asuracomic",
	"asurascans.com":       "asuracomic",
	"flamecomics.xyz":      "flamecomics",
	"mgeko.cc":             "mgeko",
	"webtoons.com":         "webtoons",
	"m.webtoons.com":       "webtoons",
	"freewebnovel.com":     "freewebnovel",
	"mangaupdates.com":     "mangaupdates",
	"api.mangaupdates.com": "mangaupdates",
	"comick.dev":           "comick",
	"comick.io":            "comick",
	"comick.fun":           "comick",
	"api.comick.dev":       "comick",
	"mangahub.io":          "mangahub",
	"api.mghcdn.com":       "mangahub",
}

// readerRanks pins the reading-chain tiering: origin scanlator sites publish
// chapters first, MangaHub is the aggregator fresh enough to beat the default
// tier, and ComicK is the floor nobody wants to read on but which always
// carries the page. The dashboard orders a tracker's linked sources by these,
// so a changed rank silently sends readers to a worse site.
var readerRanks = map[string]int{
	"asuracomic":   connectors.ReaderRankOrigin,
	"flamecomics":  connectors.ReaderRankOrigin,
	"mangahub":     connectors.ReaderRankFreshAggregator,
	"comick":       connectors.ReaderRankInfoFloor,
	"mangadex":     connectors.ReaderRankDefault,
	"mangafire":    connectors.ReaderRankDefault,
	"mgeko":        connectors.ReaderRankDefault,
	"webtoons":     connectors.ReaderRankDefault,
	"freewebnovel": connectors.ReaderRankDefault,
	"mangaupdates": connectors.ReaderRankDefault,
}

func TestRegistryResolvesEveryHistoricalHost(t *testing.T) {
	registry := connectordefaults.NewRegistry()

	for host, wantKey := range hostKeyMappings {
		// The spellings callers actually use: a bare host from a source key, a
		// site root, a stored tracker URL, and the www. variant.
		spellings := []string{
			host,
			"https://" + host,
			"https://" + host + "/series/some-title?page=2",
			"https://www." + host + "/series/some-title",
		}

		for _, spelling := range spellings {
			connector, ok := registry.Get(spelling)
			if !ok {
				t.Errorf("Get(%q): nothing resolved, want connector %q", spelling, wantKey)
				continue
			}
			if connector.Key() != wantKey {
				t.Errorf("Get(%q) resolved to %q, want %q", spelling, connector.Key(), wantKey)
			}
		}

		trackerURL := "https://" + host + "/title/123"
		connector, ok := registry.GetByURL(trackerURL)
		if !ok {
			t.Errorf("GetByURL(%q): nothing resolved, want connector %q", trackerURL, wantKey)
			continue
		}
		if connector.Key() != wantKey {
			t.Errorf("GetByURL(%q) resolved to %q, want %q", trackerURL, connector.Key(), wantKey)
		}
	}
}

func TestEveryRegisteredConnectorPublishesSiteInfo(t *testing.T) {
	registry := connectordefaults.NewRegistry()

	for _, descriptor := range registry.List() {
		connector, ok := registry.Get(descriptor.Key)
		if !ok {
			t.Fatalf("registry lists %q but cannot resolve it by key", descriptor.Key)
		}

		info, ok := connector.(connectors.SiteInfo)
		if !ok {
			t.Errorf("connector %q does not implement SiteInfo; the registry maps URLs solely through it, so every tracker stored with this site's URLs would stop resolving", descriptor.Key)
			continue
		}

		if len(info.Hosts()) == 0 {
			t.Errorf("connector %q publishes no hosts", descriptor.Key)
		}

		home := info.HomeURL()
		if strings.TrimSpace(home) == "" {
			t.Errorf("connector %q publishes no home URL", descriptor.Key)
			continue
		}
		resolved, ok := registry.GetByURL(home)
		if !ok {
			t.Errorf("connector %q home URL %q resolves to nothing; its own host is missing from Hosts()", descriptor.Key, home)
			continue
		}
		if resolved.Key() != descriptor.Key {
			t.Errorf("connector %q home URL %q resolves to %q", descriptor.Key, home, resolved.Key())
		}

		wantRank, known := readerRanks[descriptor.Key]
		if !known {
			t.Errorf("connector %q has no expected reader rank; add it to readerRanks and to the dashboard's reading chain expectations", descriptor.Key)
			continue
		}
		if got := info.ReaderRank(); got != wantRank {
			t.Errorf("connector %q ReaderRank() = %d, want %d", descriptor.Key, got, wantRank)
		}
	}
}

func TestConnectorHostClaimsDoNotOverlap(t *testing.T) {
	registry := connectordefaults.NewRegistry()

	type claim struct {
		key   string
		hosts []string
	}

	claims := make([]claim, 0)
	for _, descriptor := range registry.List() {
		connector, ok := registry.Get(descriptor.Key)
		if !ok {
			continue
		}
		info, ok := connector.(connectors.SiteInfo)
		if !ok {
			continue
		}
		claims = append(claims, claim{key: descriptor.Key, hosts: info.Hosts()})
	}

	// Two connectors claiming the same host — or one claiming a subdomain of
	// another's, which HostAllowed also matches — would make resolution depend
	// on the registry's map iteration order, so the same URL could reach a
	// different connector on every run.
	for _, owner := range claims {
		for _, other := range claims {
			if owner.key == other.key {
				continue
			}
			for _, host := range owner.hosts {
				if connectors.HostAllowed(host, other.hosts) {
					t.Errorf("host %q claimed by %q also matches %q's hosts %v; URL resolution would be nondeterministic", host, owner.key, other.key, other.hosts)
				}
			}
		}
	}
}
