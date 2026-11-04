package connectors

// BrowserUserAgent is the User-Agent every native connector presents. It lives
// here rather than in each connector because the sites this app reads treat an
// obviously outdated browser version as a bot signal, so the version has to be
// bumpable in one place. Keep it a plausible current stable Chrome on Windows.
//
// The MangaFire signer's stubbed navigator.userAgent interpolates this same
// constant: its vrf token derivation reads the value, and the token must be
// derived from the same string the HTTP header carries.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36"

// AppUserAgent is the honest User-Agent, for the hosts that refuse a spoofed
// browser. MangaDex's edge (api. and uploads. alike) has answered a browser
// User-Agent that is not backed by a browser's TLS fingerprint with a 302 to
// its bot challenge since 2026-11 — "if this isn't a browser, don't send a
// browser UA" — while any non-browser agent gets the JSON. An empty agent is
// refused with a 400, so one has to be sent.
//
// A real browser does not get through either: its requests land on the same
// challenge (a JavaScript "Browser verification" page), which an <img> tag
// cannot pass, so a cover hotlinked from uploads.mangadex.org renders broken.
const AppUserAgent = "cross-site-tracker/1.0"

// appUserAgentHosts are the hosts (subdomains included) that get AppUserAgent
// instead of BrowserUserAgent.
var appUserAgentHosts = []string{"mangadex.org"}

// UserAgentFor picks the User-Agent a request to host should carry: the
// browser one by default, the honest one for the hosts that challenge it.
func UserAgentFor(host string) string {
	if ChallengesBrowsers(host) {
		return AppUserAgent
	}
	return BrowserUserAgent
}

// ChallengesBrowsers reports whether host answers a browser with its bot
// challenge. The reader's own browser cannot load an image from such a host, so
// the dashboard relays those covers through the server instead of hotlinking
// them.
func ChallengesBrowsers(host string) bool {
	return HostAllowed(host, appUserAgentHosts)
}

// BrowserSecChUA is the Sec-Ch-Ua client-hints header that matches
// BrowserUserAgent. Chrome sends both, and their major versions must agree —
// a mismatch is itself a bot signal — so a connector that presents client
// hints must take them from here, next to the string they have to match.
const BrowserSecChUA = `"Chromium";v="139", "Google Chrome";v="139", "Not-A.Brand";v="99"`
