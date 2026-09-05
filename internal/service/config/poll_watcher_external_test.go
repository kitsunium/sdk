// Package config_test — the poll watcher as a caller wires it up.
package config_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"
)

// watchInterval is short enough to keep the tests quick and long enough that a
// loaded machine still ticks several times inside the deadline.
const watchInterval time.Duration = 10 * time.Millisecond

// watchDeadline bounds how long a test waits for a change to be observed.
const watchDeadline time.Duration = 5 * time.Second

// TestPollWatcher pins that a change to the watched file reaches the callback,
// and that cancelling the context is a clean stop rather than an error.
//
// Polling mtime and size is deliberate: it works on every GOOS with no
// inotify/kqueue dependency, and both fields are checked because an editor that
// rewrites a file in place within the same second changes the size while the
// mtime may not have moved.
//
// Goroutine lifecycle: one goroutine runs Watch, which returns when the context
// is cancelled. It reports on a buffered channel so it can never block on send,
// and every exit path cancels and receives.
func TestPollWatcher(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how the watched file is changed, after the baseline is taken.
		mutate func(t *testing.T, path string)
	}
	tests := []tc{
		{"the content grows", func(t *testing.T, path string) {
			t.Helper()
			write(t, path, []byte("initial content, now longer"))
		}},
		{"the content shrinks", func(t *testing.T, path string) {
			t.Helper()
			write(t, path, []byte("x"))
		}},
		{"the content is replaced at the same length", func(t *testing.T, path string) {
			t.Helper()
			//: same size, so only the mtime moves — which is why both fields
			//: are part of the fingerprint.
			write(t, path, []byte("INITIAL"))
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conf.json")
		write(t, path, []byte("initial"))

		var fired atomic.Int64
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- cfg.PollWatcher(path, watchInterval).Watch(ctx, func() {
				fired.Add(1)
			})
		}()

		//: give the watcher a tick to take its baseline before changing the
		//: file, so the change is genuinely observed rather than raced.
		time.Sleep(watchInterval * 2)
		c.mutate(t, path)

		deadline := time.Now().Add(watchDeadline)
		for fired.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the callback never fired after the file changed")
			}
			time.Sleep(watchInterval)
		}

		cancel()
		//: cancellation is a clean stop, not a failure: a caller shutting down
		//: must not have to distinguish it from a watch fault.
		if err := <-done; err != nil {
			t.Errorf("Watch after cancellation = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPollWatcherRejectsBadInput pins the two arguments that would otherwise
// crash the process: time.NewTicker PANICS on a non-positive interval, and
// onChange is invoked unchecked on the first detected change. Both are reachable
// from caller input, so both have to be refused rather than deferred.
func TestPollWatcherRejectsBadInput(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		interval time.Duration
		onChange func()
		//: whether the path exists; a missing file is its own watch failure.
		exists bool
	}
	tests := []tc{
		{"a zero interval", 0, func() {}, true},
		{"a negative interval", -time.Second, func() {}, true},
		{"a nil callback", watchInterval, nil, true},
		{"both wrong at once", 0, nil, true},
		{"a file that does not exist", watchInterval, func() {}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conf.json")
		if c.exists {
			write(t, path, []byte("x"))
		}

		err := cfg.PollWatcher(path, c.interval).Watch(t.Context(), c.onChange)

		//: every refusal arrives as one code, so a caller handles a bad watch
		//: the same way whatever was wrong with it.
		if !errs.HasCode(err, coreconfig.CodeConfigWatchFailed) {
			t.Fatalf("Watch(%s) = %v, want CONFIG_WATCH_FAILED", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
