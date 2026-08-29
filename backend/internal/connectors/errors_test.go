package connectors

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestHTTPStatusErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  *HTTPStatusError
		want string
	}{
		{
			name: "with the request url",
			err:  &HTTPStatusError{StatusCode: http.StatusForbidden, URL: "https://mangafire.to/manga/x"},
			want: "unexpected status 403 for https://mangafire.to/manga/x",
		},
		{
			name: "without a url",
			err:  &HTTPStatusError{StatusCode: http.StatusNotFound},
			want: "unexpected status: 404",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIsHTTPStatusThroughWrapping is the property the connectors depend on:
// they wrap a status verdict in context as it travels up ("fetch chapters: …"),
// and every caller still has to be able to classify it.
func TestIsHTTPStatusThroughWrapping(t *testing.T) {
	base := &HTTPStatusError{StatusCode: http.StatusForbidden, URL: "https://mangafire.to/manga/x"}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare", err: base, want: true},
		{name: "wrapped once", err: fmt.Errorf("fetch page: %w", base), want: true},
		{name: "wrapped twice", err: fmt.Errorf("resolve series: %w", fmt.Errorf("fetch page: %w", base)), want: true},
		{name: "wrapped three deep", err: fmt.Errorf("poll: %w", fmt.Errorf("resolve series: %w", fmt.Errorf("fetch page: %w", base))), want: true},
		{name: "joined with another error", err: errors.Join(errors.New("first attempt failed"), base), want: true},
		{
			// %v flattens the error to text, and the verdict is lost. This is
			// why the connectors have to wrap with %w.
			name: "formatted without %w",
			err:  fmt.Errorf("fetch page: %v", base),
			want: false,
		},
		{name: "plain error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsHTTPStatus(tc.err, http.StatusForbidden); got != tc.want {
				t.Fatalf("IsHTTPStatus(%v, 403) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsHTTPStatusDiscriminatesTheCode(t *testing.T) {
	err := fmt.Errorf("fetch page: %w", &HTTPStatusError{StatusCode: http.StatusTooManyRequests})

	if !IsHTTPStatus(err, http.StatusTooManyRequests) {
		t.Fatal("expected the carried code to match")
	}
	for _, other := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusOK, 0} {
		if IsHTTPStatus(err, other) {
			t.Fatalf("429 must not answer to %d", other)
		}
	}
}

// TestIsNotFound covers the one code with its own helper: a 404 is the answer
// that means "this series is gone from the site", which callers act on
// differently from every other failure.
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare 404", err: &HTTPStatusError{StatusCode: http.StatusNotFound}, want: true},
		{
			name: "wrapped 404",
			err:  fmt.Errorf("resolve: %w", fmt.Errorf("fetch page: %w", &HTTPStatusError{StatusCode: http.StatusNotFound, URL: "https://mgeko.cc/manga/x"})),
			want: true,
		},
		{name: "410 is not a 404", err: &HTTPStatusError{StatusCode: http.StatusGone}, want: false},
		{name: "site-wide block is not a 404", err: &HTTPStatusError{StatusCode: http.StatusForbidden}, want: false},
		{name: "transport failure is not a 404", err: errors.New("dial tcp: i/o timeout"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Fatalf("IsNotFound(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// TestHTTPStatusErrorAsRecoversTheURL: an operator reading the logs needs the
// URL that produced the status, and it has to survive wrapping too.
func TestHTTPStatusErrorAsRecoversTheURL(t *testing.T) {
	const url = "https://api.comick.io/comic/x/chapters"
	err := fmt.Errorf("list chapters: %w", &HTTPStatusError{StatusCode: http.StatusBadGateway, URL: url})

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected an *HTTPStatusError, got %v", err)
	}
	if statusErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want 502", statusErr.StatusCode)
	}
	if statusErr.URL != url {
		t.Fatalf("URL = %q, want %q", statusErr.URL, url)
	}
	if want := "list chapters: unexpected status 502 for " + url; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}
