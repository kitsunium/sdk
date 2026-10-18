// Package config_test — the poll watcher as a caller wires it up.
package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/app/config"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/app/config"
)

// watchInterval is the poll interval: on a ManualClock, how far one Advance
// moves it.
const watchInterval time.Duration = 10 * time.Millisecond

// watchDeadline bounds how long a test waits for a poll it caused to report.
const watchDeadline time.Duration = 5 * time.Second

// TestPollWatcher pins that a change to the watched file reaches the callback,
// and that cancelling the context is a clean stop rather than an error.
//
// Polling mtime and size is deliberate: it works on every GOOS with no
// inotify/kqueue dependency, and both fields are checked because an editor that
// rewrites a file in place within the same second changes the size while the
// mtime may not have moved.
//
// The watch ticks on a ManualClock, which is what makes the order certain:
// Watch takes its baseline before it arms its ticker, so BlockUntil(1)
// returning means the baseline exists and the change made next is observed
// rather than raced — a sleep only hoped so, and lost on a loaded Windows
// runner, where the baseline then held the change and nothing ever fired.
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
			before := modTime(t, path)
			write(t, path, []byte("INITIAL"))
			//: a filesystem stamps a write at its own granularity — a
			//: Windows tick, FAT's two seconds —, so a rewrite right after
			//: the first write may keep its mtime. The mtime is moved past
			//: any granularity, as a later edit would move it.
			if err := os.Chtimes(path, time.Time{}, before.Add(2*time.Second)); err != nil {
				t.Fatalf("moving the mtime of %s: %v", path, err)
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conf.json")
		write(t, path, []byte("initial"))

		manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
		fired := make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- cfg.PollWatcherOnClockForTest(path, watchInterval, manual).Watch(ctx, func() {
				select {
				case fired <- struct{}{}:
				default:
				}
			})
		}()

		//: the ticker is armed, so the baseline was taken before it.
		manual.BlockUntil(1)
		c.mutate(t, path)
		//: one poll, on the clock the watch ticks on.
		manual.Advance(watchInterval)

		select {
		case <-fired:
		case <-time.After(watchDeadline):
			t.Fatal("the callback never fired after the file changed")
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

// TestPollWatcherOnTheWallClock pins what PollWatcher itself builds — the
// watcher on the wall clock, the one a caller gets: it takes its baseline,
// arms its ticker and stops cleanly when its context ends. The detection is
// TestPollWatcher's, on a clock the test drives; nothing here waits for a
// state of the watch goroutine, so nothing here can race it.
func TestPollWatcherOnTheWallClock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conf.json")
	write(t, path, []byte("initial"))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- cfg.PollWatcher(path, watchInterval).Watch(ctx, func() {})
	}()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Watch after cancellation = %v, want nil", err)
	}
}

// modTime is the file's modification time.
func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// TestPollWatcherRejectsBadInput pins the two arguments that would otherwise
// crash the process: the ticker PANICS on a non-positive interval, and
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
