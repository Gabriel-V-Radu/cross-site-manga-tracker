package connectors

import "strings"

// HostAllowed reports whether host is one of allowed or a subdomain of one
// ("www.mangafire.to" belongs to "mangafire.to"). It is the shared
// implementation behind every connector's URL-ownership check.
func HostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if host == candidate || strings.HasSuffix(host, "."+candidate) {
			return true
		}
	}
	return false
}

// Reader ranks order a tracker's linked sources for chapter-link resolution
// (see SiteInfo.ReaderRank). Lower is better.
const (
	// ReaderRankOrigin marks origin scanlator sites: for their own series they
	// are where chapters appear before any aggregator mirrors them, and their
	// readers are the best of the chain.
	ReaderRankOrigin = 0
	// ReaderRankFreshAggregator marks aggregators fresh enough to beat the
	// default tier (English-only, same-day uploads).
	ReaderRankFreshAggregator = 1
	// ReaderRankDefault is every other readable site, kept in incoming order.
	ReaderRankDefault = 2
	// ReaderRankInfoFloor marks sources nobody wants to read on: they take
	// part in the chain only after every readable site and offline-built link
	// failed, valued because they always carry the chapter page.
	ReaderRankInfoFloor = 3
)

// SiteInfo is the optional metadata a connector publishes about the site it
// reads. The registry uses Hosts to map URLs and host-shaped keys to
// connectors; the dashboard's reading chain uses ReaderRank to order linked
// sources.
type SiteInfo interface {
	// Hosts returns every hostname the connector claims, including historical
	// domains and any API hosts that should map back to it. Subdomains of a
	// listed host are covered automatically (HostAllowed).
	Hosts() []string
	// HomeURL is the canonical site root, e.g. "https://mangafire.to".
	HomeURL() string
	// ReaderRank orders the site in the reading chain; one of the ReaderRank*
	// constants.
	ReaderRank() int
}
