package connectors

import "testing"

// TestUserAgentForMangaDexIsHonest: MangaDex's edge sends a spoofed browser
// agent to its bot challenge, on the API and the cover CDN alike, so every
// mangadex.org host gets the app's own agent; everything else keeps the
// browser one.
func TestUserAgentForMangaDexIsHonest(t *testing.T) {
	cases := map[string]string{
		"api.mangadex.org":     AppUserAgent,
		"uploads.mangadex.org": AppUserAgent,
		"mangadex.org":         AppUserAgent,
		"MangaDex.org":         AppUserAgent,
		"notmangadex.org":      BrowserUserAgent,
		"mangafire.to":         BrowserUserAgent,
		"127.0.0.1":            BrowserUserAgent,
		"":                     BrowserUserAgent,
	}
	for host, want := range cases {
		if got := UserAgentFor(host); got != want {
			t.Errorf("UserAgentFor(%q) = %q, want %q", host, got, want)
		}
		if got := ChallengesBrowsers(host); got != (want == AppUserAgent) {
			t.Errorf("ChallengesBrowsers(%q) = %v, disagrees with UserAgentFor", host, got)
		}
	}
}
