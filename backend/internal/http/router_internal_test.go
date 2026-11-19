package http

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// syncBuffer lets the handler goroutine write the log the test then reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// failingApp is an app wired like the server's — the same recover middleware
// and the same error handler — around handlers that fail in the ways a real
// one can: an error let through, a panic, and errors written for the client.
func failingApp(t *testing.T) (*fiber.App, *syncBuffer) {
	t.Helper()

	logged := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	app := fiber.New(fiber.Config{ErrorHandler: errorHandler})
	app.Use(recover.New(recover.Config{EnableStackTrace: true, StackTraceHandler: logPanic}))

	defect := errors.New("open /app/data/app.sqlite: database is locked")
	for _, prefix := range []string{"/dashboard", "/v1"} {
		app.Get(prefix+"/defect", func(*fiber.Ctx) error { return defect })
		app.Get(prefix+"/panic", func(*fiber.Ctx) error { panic("index out of range [3] with length 3") })
		app.Get(prefix+"/refused", func(*fiber.Ctx) error { return fiber.NewError(fiber.StatusBadRequest, "Title is required") })
		app.Get(prefix+"/unavailable", func(*fiber.Ctx) error {
			return fiber.NewError(fiber.StatusServiceUnavailable, "The source is not answering")
		})
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	return app, logged
}

func fetch(t *testing.T, app *fiber.App, path string) (int, string, string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), 10000)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return res.StatusCode, string(body), res.Header.Get("Content-Type")
}

// A failure no handler answered is a defect, and what it says can name files,
// tables and hosts. Outside /v1 it used to be sent to the browser word for
// word and written to no log at all; a panic's stack went to stderr only,
// which the server's log file never sees. Both are logged now, and the
// browser is told only that the server failed.
func TestUnhandledFailuresAreLoggedAndNotShown(t *testing.T) {
	app, logged := failingApp(t)

	for _, path := range []string{"/dashboard/defect", "/dashboard/panic", "/v1/defect", "/v1/panic"} {
		status, body, contentType := fetch(t, app, path)
		if status != http.StatusInternalServerError {
			t.Fatalf("GET %s: status %d, want 500", path, status)
		}
		for _, secret := range []string{"app.sqlite", "database is locked", "index out of range"} {
			if strings.Contains(body, secret) {
				t.Fatalf("GET %s: the answer carries the failure's own text: %q", path, body)
			}
		}
		if strings.HasPrefix(path, "/v1/") {
			if body != `{"message":"internal server error"}` || !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("GET %s: body %q (%s), want the API's JSON shape", path, body, contentType)
			}
		} else if body != "Internal Server Error" || !strings.HasPrefix(contentType, "text/plain") {
			t.Fatalf("GET %s: body %q (%s), want a plain line", path, body, contentType)
		}
	}

	log := logged.String()
	for _, want := range []string{
		`msg="http handler failed"`,
		"path=/dashboard/defect",
		"path=/v1/defect",
		"database is locked",
		`msg="http handler panicked"`,
		"path=/dashboard/panic",
		"index out of range [3] with length 3",
		// The stack names the function that panicked.
		"failingApp",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log has no %q:\n%s", want, log)
		}
	}
}

// An error written for the client is answered as it was written, in each
// route's own shape. A 5xx among them is logged; a 4xx is the normal answer
// to a bad request and is not.
func TestClientErrorsKeepTheirMessage(t *testing.T) {
	app, logged := failingApp(t)

	cases := []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{"/dashboard/refused", http.StatusBadRequest, "Title is required"},
		{"/v1/refused", http.StatusBadRequest, `{"message":"Title is required"}`},
		{"/dashboard/unavailable", http.StatusServiceUnavailable, "The source is not answering"},
		{"/v1/unavailable", http.StatusServiceUnavailable, `{"message":"The source is not answering"}`},
		{"/dashboard/no-such-route", http.StatusNotFound, "Cannot GET /dashboard/no-such-route"},
		{"/v1/no-such-route", http.StatusNotFound, `{"message":"Cannot GET /v1/no-such-route"}`},
	}
	for _, testCase := range cases {
		status, body, _ := fetch(t, app, testCase.path)
		if status != testCase.wantStatus || body != testCase.wantBody {
			t.Errorf("GET %s: %d %q, want %d %q", testCase.path, status, body, testCase.wantStatus, testCase.wantBody)
		}
	}

	log := logged.String()
	if strings.Contains(log, "/refused") || strings.Contains(log, "no-such-route") {
		t.Errorf("a 4xx was logged as a failure:\n%s", log)
	}
	if !strings.Contains(log, "path=/dashboard/unavailable") || !strings.Contains(log, "status=503") {
		t.Errorf("a 5xx written for the client was not logged:\n%s", log)
	}
}
