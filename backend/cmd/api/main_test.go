package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var errPingFailed = errors.New("ping failed")

// The watch reports only a run of failures: a success in between restarts the
// count, so a connection that was briefly busy never takes the process down.
func TestWatchDatabaseReportsOnlyConsecutiveFailures(t *testing.T) {
	cases := []struct {
		name      string
		results   []error
		wantPings int32
	}{
		{name: "every ping fails", results: []error{errPingFailed, errPingFailed, errPingFailed}, wantPings: 3},
		{name: "a success resets the count", results: []error{errPingFailed, errPingFailed, nil, errPingFailed, errPingFailed, errPingFailed}, wantPings: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var pings atomic.Int32
			ping := func(context.Context) error {
				n := int(pings.Add(1))
				if n <= len(tc.results) {
					return tc.results[n-1]
				}
				return nil
			}

			select {
			case err := <-watchDatabase(ctx, ping, time.Millisecond, time.Second, 3):
				if !errors.Is(err, errPingFailed) {
					t.Fatalf("expected the last ping's error, got %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no report after %d pings", pings.Load())
			}
			if got := pings.Load(); got != tc.wantPings {
				t.Fatalf("expected the report after %d pings, got it after %d", tc.wantPings, got)
			}
		})
	}
}

// Each ping carries its own deadline. Without one a ping waits for the pool's
// one connection forever, and a stuck connection would stall the watch that
// exists to notice it.
func TestWatchDatabaseBoundsEachPing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ping := func(pingCtx context.Context) error {
		<-pingCtx.Done()
		return pingCtx.Err()
	}

	select {
	case err := <-watchDatabase(ctx, ping, time.Millisecond, 20*time.Millisecond, 2):
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected the ping's own deadline to end it, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a ping that never returns on its own must still time out")
	}
}

// Shutdown cancels the watch, and a ping it cuts short is not a database
// failure: counting it would log a false alarm on every clean stop.
func TestWatchDatabaseIgnoresAPingCutShortByShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})
	var once atomic.Bool
	ping := func(pingCtx context.Context) error {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-pingCtx.Done()
		return pingCtx.Err()
	}

	wedged := watchDatabase(ctx, ping, time.Millisecond, time.Minute, 1)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never pinged")
	}
	cancel()

	select {
	case err := <-wedged:
		t.Fatalf("a cancelled ping must not be reported, got %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}
