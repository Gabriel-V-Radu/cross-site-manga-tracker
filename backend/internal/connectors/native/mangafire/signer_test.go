package mangafire

import (
	"net/url"
	"testing"
)

// TestSignerMatchesBrowserVectors pins the vendored bundle against tokens
// captured from mangafire.to's own getProtectionToken in a real browser. If the
// vendored signer_bundle.js is refreshed and MangaFire changed the algorithm,
// these vectors must be recaptured (see signer_bundle.README.md).
func TestSignerMatchesBrowserVectors(t *testing.T) {
	s := newSigner()

	cases := []struct {
		name   string
		path   string
		params url.Values
		want   string
	}{
		{
			name: "detail empty params",
			path: "/api/titles/ro8ro",
			want: "8sK3xtqdFdus51h8lQ",
		},
		{
			name:   "search keyword and limit",
			path:   "/api/titles",
			params: url.Values{"keyword": {"one piece"}, "limit": {"10"}},
			want:   "8sK3xtqdFZfetBhus6bRApNr5zMeEWBTZ95f9C_GdK1ylw",
		},
		{
			name:   "chapters full params",
			path:   "/api/titles/ro8ro/chapters",
			params: url.Values{"language": {"en"}, "sort": {"number"}, "order": {"desc"}, "page": {"1"}, "limit": {"20"}},
			want:   "8sK3xtqdFdus51h8lRud6HKgmNeducC3cXf3M6aNrl37GsFk0UeStLtjxL_0j7VxNduE5gwu_vQ0ICoReXe-SZNFRR_7c6VgUw",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Sign(tc.path, tc.params)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			if got != tc.want {
				t.Fatalf("token mismatch for %s\n got  %q\n want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestSignerParamOrderStable confirms the token is independent of param
// insertion order (getProtectionToken canonicalizes params), which lets the
// connector build the query string separately from signing.
func TestSignerParamOrderStable(t *testing.T) {
	s := newSigner()

	a, err := s.Sign("/api/titles", url.Values{"keyword": {"x"}, "limit": {"5"}})
	if err != nil {
		t.Fatalf("sign a: %v", err)
	}
	b, err := s.Sign("/api/titles", url.Values{"limit": {"5"}, "keyword": {"x"}})
	if err != nil {
		t.Fatalf("sign b: %v", err)
	}
	if a != b {
		t.Fatalf("token changed with param order: %q vs %q", a, b)
	}
}
