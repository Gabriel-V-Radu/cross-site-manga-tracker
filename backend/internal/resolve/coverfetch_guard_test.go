package resolve

import (
	"context"
	"net/netip"
	"net/url"
	"testing"
)

// The address guard behind the cover downloader. netip's own predicates cover
// loopback, RFC1918, link-local and multicast; the ranges listed here are the
// ones they do not, and a cover URL naming any of them is a request into this
// machine or this network dressed up as a CDN.
func TestIsPublicAddrRefusesNonRoutableRanges(t *testing.T) {
	refused := []string{
		"127.0.0.1", "10.1.2.3", "172.16.5.5", "192.168.1.20", "169.254.169.254",
		"0.0.0.0", "0.1.2.3",
		"100.64.0.1", "100.127.255.254", // CGNAT / Tailscale
		"192.0.0.9",                    // IETF protocol assignments
		"198.18.0.1", "198.19.255.255", // benchmarking
		"240.0.0.1", "255.255.255.255", // former class E, broadcast
		"::1", "fc00::1", "fe80::1", "ff02::1",
		"64:ff9b::a00:1",     // NAT64 well-known prefix
		"::ffff:192.168.0.1", // v4-mapped private
		"::ffff:100.64.0.1",  // v4-mapped CGNAT
	}
	for _, raw := range refused {
		if isPublicAddr(netip.MustParseAddr(raw)) {
			t.Errorf("%s must be refused", raw)
		}
	}

	allowed := []string{"1.1.1.1", "8.8.8.8", "100.63.255.255", "100.128.0.1", "198.17.255.255", "198.20.0.1", "2606:4700::1111"}
	for _, raw := range allowed {
		if !isPublicAddr(netip.MustParseAddr(raw)) {
			t.Errorf("%s must be allowed", raw)
		}
	}
}

// The name-based check refuses the scheme and literal addresses and nothing
// else: names are vetted by the dialer against the address actually connected
// to, so resolving them here would only double the DNS traffic per cover.
func TestCheckCoverTargetVetsSchemeAndLiterals(t *testing.T) {
	cases := []struct {
		raw    string
		refuse bool
	}{
		{"http://cdn.example/cover.jpg", true},
		{"https:///cover.jpg", true},
		{"https://127.0.0.1/cover.jpg", true},
		{"https://[::1]/cover.jpg", true},
		{"https://100.64.0.1/cover.jpg", true},
		{"https://cdn.example/cover.jpg", false},
		{"https://1.1.1.1/cover.jpg", false},
	}
	for _, tc := range cases {
		target, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		err = checkCoverTarget(context.Background(), target)
		if tc.refuse && err == nil {
			t.Errorf("%s: expected refusal", tc.raw)
		}
		if !tc.refuse && err != nil {
			t.Errorf("%s: unexpected refusal: %v", tc.raw, err)
		}
	}
}

// The guarded transport never consults a proxy: with HTTPS_PROXY set the dialer
// would vet the proxy's address and the proxy would fetch whatever the cover
// URL named.
func TestPublicAddressTransportIgnoresProxy(t *testing.T) {
	if publicAddressTransport().Proxy != nil {
		t.Fatal("the cover transport must not use a proxy")
	}
}
