// Package config — white-box tests for the poll watcher. The split between
// Watch, poll, validateInputs and stat exists so the ticker is created in the
// same function that receives from it; each half is tested where it lives.
package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tick is short enough to keep these tests quick and long enough that a loaded
// machine still ticks several times inside a deadline.
const tick time.Duration = 10 * time.Millisecond

// Test_pollWatcher_validateInputs pins the two guards that stand between caller
// input and a process crash: time.NewTicker panics on a non-positive interval,
// and onChange is invoked unchecked on the first detected change.
func Test_pollWatcher_validateInputs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		interval time.Duration
		onChange func()
		wantErr  bool
	}
	tests := []tc{
		{name: "a usable pair", interval: tick, onChange: func() {}},
		{name: "a zero interval", interval: 0, onChange: func() {}, wantErr: true},
		{name: "a negative interval", interval: -time.Second, onChange: func() {}, wantErr: true},
		{name: "a nil callback", interval: tick, wantErr: true},
		{name: "both wrong at once", interval: 0, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := pollWatcher{path: "/dev/null", interval: c.interval}.validateInputs(c.onChange)
		if c.wantErr {
			if !errs.HasCode(err, coreconfig.CodeConfigWatchFailed) {
				t.Fatalf("validateInputs(%s) = %v, want CONFIG_WATCH_FAILED", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("validateInputs(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_pollWatcher_stat pins the fingerprint. Both fields are read because
// either alone misses a real edit: a rewrite of the same length leaves the size
// unchanged, and a filesystem with coarse mtime resolution can leave the
// timestamp unchanged across a fast rewrite.
func Test_pollWatcher_stat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	type tc struct {
		name     string
		path     string
		wantSize int64
		wantErr  bool
	}
	tests := []tc{
		{name: "an existing file", path: path, wantSize: 5},
		{name: "a file that does not exist", path: filepath.Join(dir, "absent"), wantErr: true},
		//: a directory stats fine; the watcher does not require a regular file,
		//: and refusing one here would be a policy this layer does not own.
		{name: "a directory", path: dir},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		mod, size, err := pollWatcher{path: c.path, interval: tick}.stat()
		if c.wantErr {
			if err == nil {
				t.Fatalf("stat(%s) = nil, want an error", c.name)
			}
			//: a failed stat must report no fingerprint, or a caller would
			//: adopt (0, 0) as a baseline and miss the first change.
			if mod != 0 || size != 0 {
				t.Errorf("stat(%s) returned (%d, %d) beside the error", c.name, mod, size)
			}
			return
		}
		if err != nil {
			t.Fatalf("stat(%s) = %v, want nil", c.name, err)
		}
		if c.wantSize != 0 && size != c.wantSize {
			t.Errorf("stat(%s) size = %d, want %d", c.name, size, c.wantSize)
		}
		//: a zero mtime would make every later comparison meaningless.
		if mod == 0 {
			t.Errorf("stat(%s) reported a zero mtime", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_pollWatcher_poll pins the loop: it fires on a change, stops cleanly on
// cancellation, and aborts when the target disappears mid-watch.
//
// Goroutine lifecycle: one goroutine runs poll and reports on a buffered
// channel; every path cancels the context and receives, so it always joins.
func Test_pollWatcher_poll(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what happens to the file after the loop starts.
		disturb func(t *testing.T, path string)
		//: whether the loop must abort with a watch failure.
		wantErr bool
		//: whether the callback must have fired.
		wantFired bool
	}
	tests := []tc{
		{
			name:      "a changed file fires the callback",
			disturb:   func(t *testing.T, path string) { t.Helper(); rewrite(t, path, "changed and longer") },
			wantFired: true,
		},
		{
			name:    "a file removed mid-watch aborts",
			disturb: func(t *testing.T, path string) { t.Helper(); removeFile(t, path) },
			wantErr: true,
		},
		{
			name:    "an untouched file fires nothing",
			disturb: func(*testing.T, string) {},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conf")
		rewrite(t, path, "initial")

		w := pollWatcher{path: path, interval: tick}
		mod, size, err := w.stat()
		if err != nil {
			t.Fatalf("taking the baseline: %v", err)
		}

		fired := make(chan struct{}, 16)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- w.poll(ctx, func() { fired <- struct{}{} }, mod, size)
		}()

		//: let the loop reach its first tick before disturbing anything.
		time.Sleep(tick * 2)
		c.disturb(t, path)

		if c.wantErr {
			select {
			case perr := <-done:
				if !errs.HasCode(perr, coreconfig.CodeConfigWatchFailed) {
					t.Fatalf("poll = %v, want CONFIG_WATCH_FAILED", perr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("poll did not abort after the file disappeared")
			}
			return
		}

		if c.wantFired {
			select {
			case <-fired:
			case <-time.After(5 * time.Second):
				t.Fatal("the callback never fired after the file changed")
			}
		}

		cancel()
		//: cancellation is a clean stop, so a caller shutting down does not
		//: have to distinguish it from a watch fault.
		select {
		case perr := <-done:
			if perr != nil {
				t.Errorf("poll after cancellation = %v, want nil", perr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("poll did not return after cancellation")
		}
		if !c.wantFired {
			select {
			case <-fired:
				t.Error("the callback fired for an untouched file")
			default:
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_pollWatcher_Watch pins the setup half: the guards run first, then the
// baseline stat, and only then the loop — so a bad interval is refused even when
// the file is unwatchable, and vice versa.
func Test_pollWatcher_Watch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		interval time.Duration
		exists   bool
		onChange func()
	}
	tests := []tc{
		{"a bad interval on a good file", 0, true, func() {}},
		{"a nil callback on a good file", tick, true, nil},
		{"a good pair on a missing file", tick, false, func() {}},
		{"a bad interval on a missing file", 0, false, func() {}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conf")
		if c.exists {
			rewrite(t, path, "x")
		}

		err := pollWatcher{path: path, interval: c.interval}.Watch(t.Context(), c.onChange)

		//: whatever was wrong, the caller sees one code.
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

// rewrite replaces the file's contents.
func rewrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// removeFile deletes the file, so the watcher's next stat fails.
func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing %s: %v", path, err)
	}
}
