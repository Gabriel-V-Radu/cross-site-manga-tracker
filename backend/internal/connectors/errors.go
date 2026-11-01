package connectors

import (
	"errors"
	"fmt"
	"net/http"
)

// HTTPStatusError is the typed verdict for a non-2xx answer. Every connector
// returns it instead of a private status error so callers anywhere in the app
// can classify outcomes with errors.As / IsHTTPStatus regardless of which
// connector produced them.
type HTTPStatusError struct {
	StatusCode int
	// URL is the request URL the status came from; optional, but it turns
	// "unexpected status: 403" into something an operator can act on.
	URL string
}

func (e *HTTPStatusError) Error() string {
	if e.URL == "" {
		return fmt.Sprintf("unexpected status: %d", e.StatusCode)
	}
	return fmt.Sprintf("unexpected status %d for %s", e.StatusCode, e.URL)
}

// IsHTTPStatus reports whether err (or anything it wraps) is an
// HTTPStatusError carrying the given status code.
func IsHTTPStatus(err error, statusCode int) bool {
	var statusErr *HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == statusCode
}

// IsNotFound reports whether err is an HTTP 404 verdict.
func IsNotFound(err error) bool {
	return IsHTTPStatus(err, http.StatusNotFound)
}

// ErrNoChaptersListed is the verdict for a series page that loaded but parsed
// to no chapter at all. That means the site changed its markup or the page is
// not the series we think it is (a redirect to the home page), not that
// nothing new came out, so a resolve returns it as a failure: the poller then
// logs it and tries the tracker's other sources instead of quietly stamping
// the tracker as checked. Search paths, where a series with no chapter out yet
// is still a real hit, check for it with errors.Is and keep the result.
var ErrNoChaptersListed = errors.New("no chapters listed")
