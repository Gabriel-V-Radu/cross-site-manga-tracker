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

// BrowserSecChUA is the Sec-Ch-Ua client-hints header that matches
// BrowserUserAgent. Chrome sends both, and their major versions must agree —
// a mismatch is itself a bot signal — so a connector that presents client
// hints must take them from here, next to the string they have to match.
const BrowserSecChUA = `"Chromium";v="139", "Google Chrome";v="139", "Not-A.Brand";v="99"`
