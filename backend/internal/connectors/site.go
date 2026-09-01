package connectors

import (
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
)

// Site is the identity a connector embeds: its key and display name, the hosts
// it claims, the canonical origin it builds URLs on, and its place in the
// reading chain. Embedding it gives a connector Key, Name, Kind and the whole
// SiteInfo interface, plus the URL helpers below, so a connector file holds
// only what is specific to its site: how to fetch and how to parse.
//
// Ten connectors used to hand-write these methods, and drifted: nine returned
// their shared host slice by reference from Hosts() while one copied it, and
// three spelled the "does this URL belong to me" check differently. A
// connector whose canonical origin is not fixed (Asura follows the base URL it
// was constructed with, because the site has moved domain once) overrides
// HomeURL and AbsoluteURL on its own type; the embedded methods are defaults.
type Site struct {
	SiteKey  string
	SiteName string
	// SiteHosts is every hostname the connector claims — the live domain,
	// historical ones trackers were linked on, API hosts that should map back
	// to it. Subdomains are covered (HostAllowed).
	SiteHosts []string
	// Home is the canonical origin every returned or stored URL is built on,
	// e.g. "https://flamecomics.xyz". Deliberately not where requests go: that
	// is a test server in tests, while these URLs are stored in trackers and
	// opened in the reader's browser.
	Home string
	// Rank orders the site in the reading chain; one of the ReaderRank*
	// constants.
	Rank int
}

func (s Site) Key() string  { return s.SiteKey }
func (s Site) Name() string { return s.SiteName }
func (s Site) Kind() string { return KindNative }

// Hosts implements SiteInfo. A copy, so a caller cannot rewrite the
// connector's host list through the slice it was handed.
func (s Site) Hosts() []string { return slices.Clone(s.SiteHosts) }

// HomeURL implements SiteInfo.
func (s Site) HomeURL() string { return s.Home }

// ReaderRank implements SiteInfo.
func (s Site) ReaderRank() int { return s.Rank }

// AllowsHost reports whether host is one this site claims.
func (s Site) AllowsHost(host string) bool { return HostAllowed(host, s.SiteHosts) }

// ParseOwnedURL parses raw and refuses it unless its host is one the site
// claims. Every connector's ResolveByURL and ResolveChapterURL open with this
// check; the error text names the site so a failed resolve in the log says
// which connector was asked about a URL that was not its own.
func (s Site) ParseOwnedURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if !s.AllowsHost(parsed.Hostname()) {
		return nil, fmt.Errorf("url does not belong to %s", s.SiteKey)
	}
	return parsed, nil
}

// AbsoluteURL resolves a scraped href against the site's canonical origin.
func (s Site) AbsoluteURL(raw string) string { return AbsoluteURL(s.Home, raw) }

// AbsoluteURL turns an href as it appears in a page into an absolute URL on
// base: absolute URLs pass through, protocol-relative ones get https, paths
// are joined to base. Empty stays empty.
func AbsoluteURL(base string, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "//") {
		return "https:" + trimmed
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasPrefix(trimmed, "/") {
		return base + trimmed
	}
	return base + "/" + trimmed
}

// PathSegments returns the cleaned path of u split on "/", without empty
// segments: "/series/123/" → ["series", "123"]. A root or empty path yields
// nil, so callers can test len() without special-casing "".
func PathSegments(u *url.URL) []string {
	if u == nil {
		return nil
	}
	cleaned := strings.Trim(path.Clean("/"+u.Path), "/")
	if cleaned == "" {
		return nil
	}
	return strings.Split(cleaned, "/")
}
