package handlers_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The pool holds one connection, so a probe made while something else holds
// it must give up and say so. A Ping without a deadline used to wait for the
// connection forever: the probe never answered and its goroutine stayed
// blocked, one more for every minute the healthcheck kept asking.
func TestHealthAnswersDegradedWhenTheConnectionIsHeld(t *testing.T) {
	db, app, cleanup := setupTestApp(t)
	defer cleanup()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Rolled back by hand below; the defer covers a Fatalf in between.
	defer tx.Rollback()

	started := time.Now()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil), 10000)
	if err != nil {
		t.Fatalf("GET /health with the connection held: %v", err)
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 503 while the connection is held, got %d (body: %s)", res.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the probe must give up on its own deadline, took %s", elapsed)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	res, err = app.Test(httptest.NewRequest(http.MethodGet, "/health", nil), 10000)
	if err != nil {
		t.Fatalf("GET /health after release: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 once the connection is free, got %d", res.StatusCode)
	}
}
