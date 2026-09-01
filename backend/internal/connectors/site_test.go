package connectors

import (
	"net/url"
	"strings"
	"testing"
)

func testSite() Site {
	return Site{
		SiteKey:   "flamecomics",
		SiteName:  "FlameComics",
		SiteHosts: []string{"flamecomics.xyz", "flamescans.org"},
		Home:      "https://flamecomics.xyz",
		Rank:      ReaderRankOrigin,
	}
}

func TestSiteImplementsConnectorIdentityAndSiteInfo(t *testing.T) {
	site := testSite()
	var info SiteInfo = site
	if site.Key() != "flamecomics" || site.Name() != "FlameComics" || site.Kind() != KindNative {
		t.Fatalf("identity = %q %q %q", site.Key(), site.Name(), site.Kind())
	}
	if info.HomeURL() != "https://flamecomics.xyz" || info.ReaderRank() != ReaderRankOrigin {
		t.Fatalf("site info = %q %d", info.HomeURL(), info.ReaderRank())
	}
}

// Hosts hands out a copy: a caller that appends to or rewrites the slice must
// not change which URLs the connector claims.
func TestSiteHostsIsACopy(t *testing.T) {
	site := testSite()
	hosts := site.Hosts()
	hosts[0] = "evil.example"
	if site.SiteHosts[0] != "flamecomics.xyz" {
		t.Fatal("Hosts() must not alias the connector's host list")
	}
	if !site.AllowsHost("www.flamescans.org") || site.AllowsHost("evil.example") {
		t.Fatal("AllowsHost must follow the connector's own list")
	}
}

func TestSiteParseOwnedURL(t *testing.T) {
	site := testSite()

	parsed, err := site.ParseOwnedURL("  https://www.flamecomics.xyz/series/123/  ")
	if err != nil {
		t.Fatalf("own url refused: %v", err)
	}
	if got := PathSegments(parsed); len(got) != 2 || got[0] != "series" || got[1] != "123" {
		t.Fatalf("segments = %v", got)
	}

	cases := map[string]string{
		"":                                "url is required",
		"   ":                             "url is required",
		"https://mangadex.org/title/x":    "does not belong to flamecomics",
		"http://[::1]:namedport/series/1": "invalid url",
		"https://flamecomics.xyz.evil.example/series/1": "does not belong to flamecomics",
	}
	for raw, want := range cases {
		_, err := site.ParseOwnedURL(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseOwnedURL(%q) error = %v, want %q", raw, err, want)
		}
	}
}

func TestAbsoluteURL(t *testing.T) {
	cases := []struct {
		base, raw, want string
	}{
		{"https://flamecomics.xyz", "", ""},
		{"https://flamecomics.xyz", "   ", ""},
		{"https://flamecomics.xyz", "https://cdn.example/a.webp", "https://cdn.example/a.webp"},
		{"https://flamecomics.xyz", "http://cdn.example/a.webp", "http://cdn.example/a.webp"},
		{"https://flamecomics.xyz", "//cdn.example/a.webp", "https://cdn.example/a.webp"},
		{"https://flamecomics.xyz", "/series/1", "https://flamecomics.xyz/series/1"},
		{"https://flamecomics.xyz/", "/series/1", "https://flamecomics.xyz/series/1"},
		{"https://flamecomics.xyz", "series/1", "https://flamecomics.xyz/series/1"},
	}
	for _, tc := range cases {
		if got := AbsoluteURL(tc.base, tc.raw); got != tc.want {
			t.Errorf("AbsoluteURL(%q, %q) = %q, want %q", tc.base, tc.raw, got, tc.want)
		}
	}
	if got := testSite().AbsoluteURL("/series/1"); got != "https://flamecomics.xyz/series/1" {
		t.Errorf("Site.AbsoluteURL = %q", got)
	}
}

func TestPathSegments(t *testing.T) {
	cases := map[string][]string{
		"https://x.example":                 nil,
		"https://x.example/":                nil,
		"https://x.example/series/123/":     {"series", "123"},
		"https://x.example//series///123":   {"series", "123"},
		"https://x.example/a/./b/../c":      {"a", "c"},
		"https://x.example/manga/one.dkw?q": {"manga", "one.dkw"},
	}
	for raw, want := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		got := PathSegments(parsed)
		if len(got) != len(want) {
			t.Errorf("PathSegments(%q) = %v, want %v", raw, got, want)
			continue
		}
		for index := range want {
			if got[index] != want[index] {
				t.Errorf("PathSegments(%q) = %v, want %v", raw, got, want)
				break
			}
		}
	}
	if PathSegments(nil) != nil {
		t.Error("nil url must yield nil")
	}
}
