package connectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// headerRecorder captures what a test server actually received. The handler
// runs on the server's goroutine, so the capture is mutex-guarded.
type headerRecorder struct {
	mu     sync.Mutex
	header http.Header
}

func (h *headerRecorder) record(header http.Header) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.header = header.Clone()
}

func (h *headerRecorder) get(name string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.header.Get(name)
}

// endlessReader stands in for a response that never ends: only the bound can
// stop the read. It refuses to serve past twice the default cap so that a
// regression which drops the bound fails as a test error, instead of reading
// until it exhausts the machine — the failure this reader exists to catch is
// exactly the one that would otherwise take the developer's machine down with it.
type endlessReader struct{ served int64 }

func (e *endlessReader) Read(p []byte) (int, error) {
	if e.served > 2*DefaultMaxFetchBytes {
		return 0, fmt.Errorf("read %d bytes without stopping: the caller is not bounding the read", e.served)
	}
	for i := range p {
		p[i] = 'x'
	}
	e.served += int64(len(p))
	return len(p), nil
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func newRequest(t *testing.T, method string, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	return req
}

// TestFetchBytesBoundsTheBody is the memory-safety property the shared fetch
// helpers exist for: a site that answers with far more than expected must cost
// the caller the cap, not the whole response.
func TestFetchBytesBoundsTheBody(t *testing.T) {
	const served = 64 << 10
	payload := strings.Repeat("a", served)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, payload)
	}))
	defer srv.Close()

	const bound = 128
	body, _, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL), bound)
	if err != nil {
		t.Fatalf("FetchBytes: %v", err)
	}
	if len(body) != bound {
		t.Fatalf("read %d bytes, want the body truncated to the %d-byte cap", len(body), bound)
	}
	if string(body) != payload[:bound] {
		t.Fatal("the truncated body must be the leading bytes of the response")
	}
}

// TestFetchBytesTruncationIsSilent pins that a body cut short by the bound is
// not an error: a caller that needs to know has to check the length itself.
func TestFetchBytesTruncationIsSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"chapter":"1044.5"}`)
	}))
	defer srv.Close()

	body, _, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL), 4)
	if err != nil {
		t.Fatalf("a truncated read must not report an error, got %v", err)
	}
	if string(body) != `{"ch` {
		t.Fatalf("body = %q, want the first 4 bytes", body)
	}
}

func TestFetchBytesUnboundedReadsTheWholeBody(t *testing.T) {
	payload := strings.Repeat("chapter-", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, payload)
	}))
	defer srv.Close()

	for _, maxBytes := range []int64{0, -1} {
		body, _, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL), maxBytes)
		if err != nil {
			t.Fatalf("FetchBytes(maxBytes=%d): %v", maxBytes, err)
		}
		if string(body) != payload {
			t.Fatalf("maxBytes=%d returned %d bytes, want the whole %d-byte body", maxBytes, len(body), len(payload))
		}
	}
}

func TestReadBodyLimited(t *testing.T) {
	const payload = "0123456789"

	cases := []struct {
		name     string
		maxBytes int64
		want     string
	}{
		{name: "cap below the body", maxBytes: 4, want: "0123"},
		{name: "cap at the body length", maxBytes: 10, want: payload},
		{name: "cap above the body", maxBytes: 1000, want: payload},
		{name: "one byte", maxBytes: 1, want: "0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadBodyLimited(strings.NewReader(payload), tc.maxBytes)
			if err != nil {
				t.Fatalf("ReadBodyLimited: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("ReadBodyLimited(%d) = %q, want %q", tc.maxBytes, got, tc.want)
			}
		})
	}
}

// TestReadBodyLimitedFallsBackToTheDefaultCap: an unset bound must mean the
// default, never "unbounded" — a caller that forgets to pass one is still
// protected from a response that does not stop.
func TestReadBodyLimitedFallsBackToTheDefaultCap(t *testing.T) {
	for _, maxBytes := range []int64{0, -1} {
		got, err := ReadBodyLimited(&endlessReader{}, maxBytes)
		if err != nil {
			t.Fatalf("ReadBodyLimited(maxBytes=%d): %v", maxBytes, err)
		}
		if int64(len(got)) != DefaultMaxFetchBytes {
			t.Fatalf("maxBytes=%d read %d bytes, want the %d-byte default cap", maxBytes, len(got), DefaultMaxFetchBytes)
		}
	}
}

func TestReadBodyLimitedPropagatesReadErrors(t *testing.T) {
	sentinel := errors.New("connection reset by peer")
	if _, err := ReadBodyLimited(failingReader{err: sentinel}, 1024); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the underlying read error", err)
	}
}

// TestFetchBytesTypedStatusError: every connector classifies outcomes off this
// type, so a non-2xx has to arrive as *HTTPStatusError carrying the code and
// the URL rather than as a generic error.
func TestFetchBytesTypedStatusError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, "<html>blocked</html>")
		}))

		body, finalURL, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL+"/manga/x"), 0)
		srv.Close()

		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("status %d: err = %v, want an *HTTPStatusError", status, err)
		}
		if statusErr.StatusCode != status {
			t.Fatalf("StatusCode = %d, want %d", statusErr.StatusCode, status)
		}
		if statusErr.URL != srv.URL+"/manga/x" {
			t.Fatalf("URL = %q, want %q", statusErr.URL, srv.URL+"/manga/x")
		}
		if !IsHTTPStatus(err, status) {
			t.Fatalf("IsHTTPStatus(err, %d) = false", status)
		}
		// The error body is not handed back: callers must not mistake a block
		// page for content.
		if body != nil {
			t.Fatalf("body = %q, want nil on a status failure", body)
		}
		if finalURL == "" {
			t.Fatal("the final URL must still be reported on a status failure")
		}
	}
}

// TestFetchBytesAcceptsTheWhole2xxRange guards the success test itself: some of
// these sites answer 204 for an empty result, which is not a failure.
func TestFetchBytesAcceptsTheWhole2xxRange(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "200", status: http.StatusOK, body: "ok"},
		{name: "201", status: http.StatusCreated, body: "created"},
		{name: "204", status: http.StatusNoContent, body: ""},
		{name: "299", status: 299, body: "odd but successful"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			body, _, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL), 0)
			if err != nil {
				t.Fatalf("status %d must be a success, got %v", tc.status, err)
			}
			if string(body) != tc.body {
				t.Fatalf("body = %q, want %q", body, tc.body)
			}
		})
	}
}

// TestFetchBytesFillsStandardHeadersOnlyWhenAbsent: connectors with a
// site-specific header set (MangaFire's signed requests, FreeWebNovel's TLS
// profile) must keep full control of what the site sees.
func TestFetchBytesFillsStandardHeadersOnlyWhenAbsent(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	if _, _, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL), 0); err != nil {
		t.Fatalf("FetchBytes: %v", err)
	}
	if got := recorder.get("User-Agent"); got != BrowserUserAgent {
		t.Fatalf("User-Agent = %q, want the shared browser agent", got)
	}
	if got := recorder.get("Accept-Language"); got != "en-US,en;q=0.9" {
		t.Fatalf("Accept-Language = %q", got)
	}
	// Accept is left to the caller: FetchBytes is used for HTML, JSON and
	// binary responses alike.
	if got := recorder.get("Accept"); got != "" {
		t.Fatalf("Accept = %q, want it left unset", got)
	}

	req := newRequest(t, http.MethodGet, srv.URL)
	req.Header.Set("User-Agent", "custom-agent/1.0")
	req.Header.Set("Accept-Language", "ja")
	if _, _, err := FetchBytes(srv.Client(), req, 0); err != nil {
		t.Fatalf("FetchBytes: %v", err)
	}
	if got := recorder.get("User-Agent"); got != "custom-agent/1.0" {
		t.Fatalf("User-Agent = %q, want the caller's own", got)
	}
	if got := recorder.get("Accept-Language"); got != "ja" {
		t.Fatalf("Accept-Language = %q, want the caller's own", got)
	}
}

// TestFetchBytesReportsTheURLAfterRedirects: sites answer a search with a
// redirect to the canonical series URL, and connectors read the series id back
// out of the URL they landed on.
func TestFetchBytesReportsTheURLAfterRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/series/nano-machine", http.StatusFound)
			return
		}
		io.WriteString(w, "landed")
	}))
	defer srv.Close()

	body, finalURL, err := FetchBytes(srv.Client(), newRequest(t, http.MethodGet, srv.URL+"/start"), 0)
	if err != nil {
		t.Fatalf("FetchBytes: %v", err)
	}
	if string(body) != "landed" {
		t.Fatalf("body = %q", body)
	}
	if want := srv.URL + "/series/nano-machine"; finalURL != want {
		t.Fatalf("finalURL = %q, want %q", finalURL, want)
	}
}

// TestFetchBytesTransportFailure: a connection that never answers is not a
// status verdict, and must not be classified as one.
func TestFetchBytesTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := srv.Client()
	url := srv.URL
	srv.Close()

	body, _, err := FetchBytes(client, newRequest(t, http.MethodGet, url), 0)
	if err == nil {
		t.Fatal("expected an error from a closed server")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil", body)
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		t.Fatalf("a transport failure must not read as a status verdict, got %v", err)
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("err = %v, want it named as a request failure", err)
	}
}

func TestFetchHTML(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header)
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, "<html><h1>Nano Machine</h1></html>")
	}))
	defer srv.Close()

	page, err := FetchHTML(context.Background(), srv.Client(), srv.URL+"/series")
	if err != nil {
		t.Fatalf("FetchHTML: %v", err)
	}
	if page != "<html><h1>Nano Machine</h1></html>" {
		t.Fatalf("page = %q", page)
	}
	// The sites' bot checks look at Accept: a page navigation announces HTML.
	if got := recorder.get("Accept"); got != acceptHTML {
		t.Fatalf("Accept = %q, want the browser page-navigation value", got)
	}

	if _, err := FetchHTML(context.Background(), srv.Client(), srv.URL+"/gone"); !IsNotFound(err) {
		t.Fatalf("err = %v, want a 404 verdict that survives FetchHTML", err)
	}
}

func TestFetchJSONDecodes(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header)
		io.WriteString(w, `{"title":"Nano Machine","chapters":[{"number":241.5}]}`)
	}))
	defer srv.Close()

	var payload struct {
		Title    string `json:"title"`
		Chapters []struct {
			Number float64 `json:"number"`
		} `json:"chapters"`
	}
	if err := FetchJSON(context.Background(), srv.Client(), srv.URL+"/api/series", &payload); err != nil {
		t.Fatalf("FetchJSON: %v", err)
	}
	if payload.Title != "Nano Machine" {
		t.Fatalf("Title = %q", payload.Title)
	}
	if len(payload.Chapters) != 1 || payload.Chapters[0].Number != 241.5 {
		t.Fatalf("Chapters = %+v", payload.Chapters)
	}
	if got := recorder.get("Accept"); got != acceptJSON {
		t.Fatalf("Accept = %q, want %q", got, acceptJSON)
	}
	if got := recorder.get("User-Agent"); got != BrowserUserAgent {
		t.Fatalf("the JSON path must present the same browser agent, got %q", got)
	}
}

// TestDoJSONKeepsCallerSuppliedAccept: ComicK and MangaHub ask for their own
// content types, and the default must not overwrite them.
func TestDoJSONKeepsCallerSuppliedAccept(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	req := newRequest(t, http.MethodPost, srv.URL+"/graphql")
	req.Header.Set("Accept", "application/graphql-response+json")
	req.Header.Set("Content-Type", "application/json")

	var payload map[string]any
	if err := DoJSON(srv.Client(), req, &payload, 0); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if got := recorder.get("Accept"); got != "application/graphql-response+json" {
		t.Fatalf("Accept = %q, want the caller's own", got)
	}
}

// TestDoJSONSeparatesDecodeFailuresFromStatusFailures: the two mean different
// things to a connector — a status verdict is the site refusing, a decode
// failure is the site's shape having changed — and must stay distinguishable.
func TestDoJSONSeparatesDecodeFailuresFromStatusFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html-instead-of-json":
			io.WriteString(w, "<html>Just a moment...</html>")
		case "/blocked":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":"forbidden"}`)
		}
	}))
	defer srv.Close()

	var payload map[string]any

	err := FetchJSON(context.Background(), srv.Client(), srv.URL+"/html-instead-of-json", &payload)
	if err == nil {
		t.Fatal("expected a decode failure")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("err = %v, want it named as a decode failure", err)
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		t.Fatalf("a decode failure must not read as a status verdict, got %v", err)
	}

	err = FetchJSON(context.Background(), srv.Client(), srv.URL+"/blocked", &payload)
	if !IsHTTPStatus(err, http.StatusForbidden) {
		t.Fatalf("err = %v, want a 403 verdict", err)
	}
	if strings.Contains(err.Error(), "decode response") {
		t.Fatalf("a refused request must not be reported as a decode failure: %v", err)
	}
}

// TestDoJSONHonorsMaxBytes: the bound applies before decoding, so a body cut
// short surfaces as a decode failure rather than a silently partial object.
func TestDoJSONHonorsMaxBytes(t *testing.T) {
	body := `{"title":"Nano Machine"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()

	var payload map[string]any
	err := DoJSON(srv.Client(), newRequest(t, http.MethodGet, srv.URL), &payload, int64(len(body)-3))
	if err == nil {
		t.Fatal("expected a truncated body to fail decoding")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("err = %v, want a decode failure", err)
	}

	if err := DoJSON(srv.Client(), newRequest(t, http.MethodGet, srv.URL), &payload, int64(len(body))); err != nil {
		t.Fatalf("a bound at exactly the body length must succeed, got %v", err)
	}
	if payload["title"] != "Nano Machine" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestFetchJSONRejectsAnUnusableEndpoint(t *testing.T) {
	var payload map[string]any
	err := FetchJSON(context.Background(), http.DefaultClient, "://not-a-url", &payload)
	if err == nil {
		t.Fatal("expected an error for an unparseable endpoint")
	}
	if !strings.Contains(err.Error(), "create request") {
		t.Fatalf("err = %v, want it named as a request-construction failure", err)
	}
}

// TestFetchBytesSendsTheRequestBody guards the POST paths (GraphQL, MangaFire's
// signed queries) against the shared helper consuming or dropping the body.
func TestFetchBytesSendsTheRequestBody(t *testing.T) {
	received := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received <- string(raw)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader([]byte(`{"query":"{ me }"}`)))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if _, _, err := FetchBytes(srv.Client(), req, 0); err != nil {
		t.Fatalf("FetchBytes: %v", err)
	}
	if got := <-received; got != `{"query":"{ me }"}` {
		t.Fatalf("server received %q", got)
	}
}
