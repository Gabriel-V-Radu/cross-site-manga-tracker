package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxFetchBytes caps how much of a response body a fetch helper will
// read when the caller does not pick its own bound. The sites this app reads
// serve pages and payloads in the tens-of-KB range; the cap is a safety bound
// against a misbehaving or hostile response, not a size estimate.
const DefaultMaxFetchBytes = 16 << 20

const (
	acceptHTML     = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	acceptJSON     = "application/json"
	acceptLanguage = "en-US,en;q=0.9"
)

// FetchBytes executes req and returns the response body, bounded by maxBytes
// (DefaultMaxFetchBytes when <= 0), plus the final URL after redirects. The
// standard browser headers (User-Agent, Accept-Language) are applied only when
// the request does not already carry them, so connectors with site-specific
// header sets keep full control. The User-Agent is the one UserAgentFor picks
// for the request's host. A non-2xx answer is returned as an *HTTPStatusError,
// and an answer whose redirects ended on another site (SameSite) as an
// *OffSiteRedirectError, whatever its status: it is not the resource asked for.
func FetchBytes(client *http.Client, req *http.Request, maxBytes int64) ([]byte, string, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgentFor(req.URL.Hostname()))
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}

	res, err := client.Do(req)
	if err != nil {
		// A client that refuses off-site hops (RefuseOffSiteRedirect) reports
		// one here, wrapped in the request's url.Error; it is handed back as
		// the same bare verdict the check below gives a client that followed
		// the hop.
		var offSite *OffSiteRedirectError
		if errors.As(err, &offSite) {
			return nil, "", offSite
		}
		return nil, "", fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	finalURL := req.URL.String()
	if res.Request != nil && res.Request.URL != nil {
		finalURL = res.Request.URL.String()
		if from, to := req.URL.Hostname(), res.Request.URL.Hostname(); !SameSite(from, to) {
			return nil, finalURL, &OffSiteRedirectError{From: from, To: to}
		}
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, finalURL, &HTTPStatusError{StatusCode: res.StatusCode, URL: finalURL}
	}

	body, err := ReadBodyLimited(res.Body, maxBytes)
	if err != nil {
		return nil, finalURL, fmt.Errorf("read response body: %w", err)
	}
	return body, finalURL, nil
}

// ReadBodyLimited reads body through an io.LimitReader bounded at maxBytes
// (DefaultMaxFetchBytes when <= 0). It exists for the few fetch paths that
// cannot go through FetchBytes (MangaFire classifies error responses by header
// and body) but must still never read an unbounded body.
func ReadBodyLimited(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFetchBytes
	}
	return io.ReadAll(io.LimitReader(body, maxBytes))
}

// FetchHTML fetches endpoint as a browser page navigation and returns the body
// as a string. This is the shared implementation of the HTML-scraping
// connectors' fetchPage.
func FetchHTML(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", acceptHTML)

	body, _, err := FetchBytes(client, req, 0)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// DoJSON executes a prepared request and decodes the JSON response into
// target, reading at most maxBytes (DefaultMaxFetchBytes when <= 0). Use it
// when the request needs site-specific headers or a body; FetchJSON covers
// the plain-GET case.
func DoJSON(client *http.Client, req *http.Request, target any, maxBytes int64) error {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", acceptJSON)
	}

	body, _, err := FetchBytes(client, req, maxBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// FetchJSON GETs endpoint and decodes the JSON response into target.
func FetchJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	return DoJSON(client, req, target, 0)
}
