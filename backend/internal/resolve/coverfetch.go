package resolve

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"image"
	// Registered for imageDimensions: these are the formats whose shape can be
	// measured before a cover is accepted.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gabriel/cross-site-tracker/backend/internal/connectors"
)

// coverProbeClient fetches candidate cover images without the guard below. It
// goes through the shared throttle like everything else; image CDNs are their
// own hosts, so the pacing does not compete with API traffic.
var coverProbeClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: connectors.ThrottleTransport(nil),
}

// guardedCoverClient is the client the running app fetches covers with, and the
// one a resolver gets when its config names none. A cover URL is scraped from a
// third-party page and the image it returns is then republished under /covers,
// so an unrestricted fetch hands whoever controls a source a request into the
// Pi's own services and the rest of the LAN. Three checks stand between them:
// the transport refuses anything that is not https to a name resolving entirely
// to public addresses, the dialer refuses the address the connection is
// actually being made to, and CheckRedirect refuses a hop before it is issued.
var guardedCoverClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &publicHostTransport{base: connectors.ThrottleTransport(publicAddressTransport())},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after %d redirects", len(via))
		}
		return checkCoverTarget(req.Context(), req.URL)
	},
}

// publicHostTransport refuses a request before the throttle claims a pacing
// slot for it. It sits outside the throttle so a refused target never delays a
// real fetch, and it runs on every request the client makes, redirects included.
type publicHostTransport struct {
	base http.RoundTripper
}

func (t *publicHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkCoverTarget(req.Context(), req.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// publicAddressTransport dials public addresses only. Unlike the name-based
// check this one sees the address the connection is actually being made to, so
// a name that answers with a public address and then a loopback one is refused
// on the answer that would have been used.
func publicAddressTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("cover fetch: unusable dial address %q", address)
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("cover fetch: unusable dial address %q", address)
			}
			if !isPublicAddr(addr) {
				return fmt.Errorf("cover fetch: refusing to connect to %s", addr)
			}
			return nil
		},
	}).DialContext
	return transport
}

// checkCoverTarget rejects a cover URL that is not https or that names a host
// with any non-public answer. All answers must be public: a name that resolves
// to both is a rebind attempt, not a CDN.
func checkCoverTarget(ctx context.Context, target *url.URL) error {
	if target == nil || !strings.EqualFold(target.Scheme, "https") {
		return fmt.Errorf("cover fetch: refusing non-https url")
	}

	host := target.Hostname()
	if host == "" {
		return fmt.Errorf("cover fetch: url without a host")
	}

	if literal, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(literal) {
			return fmt.Errorf("cover fetch: refusing address %s", literal)
		}
		return nil
	}

	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("cover fetch: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("cover fetch: %s resolves to nothing", host)
	}
	for _, addr := range addrs {
		if !isPublicAddr(addr) {
			return fmt.Errorf("cover fetch: %s resolves to %s", host, addr)
		}
	}
	return nil
}

// isPublicAddr reports whether an address is one the public internet routes to.
// Everything it rejects — loopback, RFC1918 and unique-local, link-local,
// multicast, unspecified — is this machine or this network.
func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	switch {
	case addr.IsLoopback(),
		addr.IsPrivate(),
		addr.IsUnspecified(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast():
		return false
	}
	return true
}

// coverDownloadLimit caps how much of a claimed cover is read. Real covers
// run tens to a few hundred KB; anything past the cap is not a cover.
const coverDownloadLimit = 8 << 20

// storeLocally downloads a cover image into the resolver's directory and
// returns the file name it was stored under. The name is the hash of the remote
// URL, so the same art shared by several trackers is stored once, and the
// /covers route can serve it as immutable — a source that changes its art
// publishes a new URL, which becomes a new file.
func (r *CoverResolver) storeLocally(parent context.Context, coverURL string) (string, bool) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")

	res, err := r.client.Do(req)
	if err != nil {
		slog.Debug("cover download failed", "url", coverURL, "error", err)
		return "", false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, coverDownloadLimit+1))
	if err != nil || len(body) == 0 || len(body) > coverDownloadLimit {
		return "", false
	}

	ext := coverFileExt(res.Header.Get("Content-Type"), body)
	if ext == "" {
		// Not recognizably an image — an HTML challenge page, a CDN error
		// body. Caching it would serve garbage forever.
		return "", false
	}

	if width, height, measured := imageDimensions(body); measured && !coverShaped(width, height) {
		// Cover art is portrait. A square image on a cover endpoint is a promo
		// banner or a site placeholder, and accepting one ends the fallback
		// chain on something that is not the cover — the caller can still find
		// the real art on one of the tracker's other linked sources.
		slog.Debug("cover rejected as not cover-shaped", "url", coverURL, "width", width, "height", height)
		return "", false
	}

	name := fmt.Sprintf("%x%s", sha1.Sum([]byte(coverURL)), ext)
	target := filepath.Join(r.dir, name)
	if _, statErr := os.Stat(target); statErr == nil {
		return name, true
	}

	// Write-then-rename so a concurrent fetch of the same URL never serves a
	// half-written file. A rename that loses the race to an identical file is
	// a success.
	temp, err := os.CreateTemp(r.dir, name+".tmp*")
	if err != nil {
		return "", false
	}
	tempName := temp.Name()
	_, writeErr := temp.Write(body)
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tempName)
		return "", false
	}
	if err := os.Rename(tempName, target); err != nil {
		_ = os.Remove(tempName)
		if _, statErr := os.Stat(target); statErr != nil {
			return "", false
		}
	}
	return name, true
}

// coverAspectFloor is how portrait a downloaded image must be to pass as cover
// art. The library on disk runs from 1.33 to 1.56 tall-over-wide, so the floor
// clears every real cover with room to spare while still catching the square
// thumbnails some sites serve from their cover endpoint.
const coverAspectFloor = 1.2

// imageDimensions reads an image header without decoding the pixels. It
// reports measured=false for a format the standard library cannot read — webp
// and avif, which much of the library is stored in — because a shape that
// cannot be measured must not be judged.
func imageDimensions(body []byte) (width int, height int, measured bool) {
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func coverShaped(width int, height int) bool {
	return float64(height)/float64(width) >= coverAspectFloor
}

// coverFileExt maps a downloaded cover to the extension the static route
// serves it under, trusting the declared content type first and the bytes
// second. An empty result means "not an image we recognize".
func coverFileExt(contentType string, body []byte) string {
	declared := strings.ToLower(strings.TrimSpace(contentType))
	if semicolon := strings.IndexByte(declared, ';'); semicolon >= 0 {
		declared = strings.TrimSpace(declared[:semicolon])
	}
	byType := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"image/avif": ".avif",
	}
	if ext, ok := byType[declared]; ok {
		return ext
	}
	if ext, ok := byType[strings.ToLower(http.DetectContentType(body))]; ok {
		return ext
	}
	return ""
}

// probe asks the image host for the first byte of the file. The timeout is
// deliberately short next to the resolve timeout: the failure mode this guards
// against is a dead CDN that hangs the connection (not a slow answer), and
// every broken cover on a page pays it serially per host.
func (r *CoverResolver) probe(parent context.Context, coverURL string) bool {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", connectors.BrowserUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	req.Header.Set("Range", "bytes=0-0")

	res, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))

	// Any refusal counts as broken: the dashboard hotlinks covers with the
	// reader's plain browser request, so a host that turns us down here would
	// turn the <img> tag down too.
	return res.StatusCode >= 200 && res.StatusCode < 300
}
