// Package config — cross-OS poll watcher.
package config

import (
	"context"
	"errors"
	"os"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
)

// errNonPositiveInterval / errNilOnChange are the local causes carried into
// CONFIG_WATCH_FAILED by Watch's input guards. They are plain stdlib errors on
// purpose: wrapAs only reads cause.Error() into a field, so these never become
// an error origin and cannot collide with the SDK code registry.
var (
	errNonPositiveInterval = errors.New("poll interval must be > 0")
	errNilOnChange         = errors.New("onChange callback must not be nil")
)

// pollWatcher detects file changes by polling its mtime+size on an interval —
// cross-OS (os.Stat works on every GOOS; no inotify/kqueue dependency).
type pollWatcher struct {
	path     string
	interval time.Duration
}

// PollWatcher returns a Watcher that polls path every interval and invokes the
// onChange callback when the file's mtime or size changes.
func PollWatcher(path string, interval time.Duration) coreconfig.Watcher {
	//: a stateless watcher bound to a path + interval.
	return pollWatcher{path: path, interval: interval}
}

// Watch polls until ctx is cancelled, firing onChange on each detected change.
func (w pollWatcher) Watch(ctx context.Context, onChange func()) error {
	//: time.NewTicker PANICS on a non-positive interval, and onChange is
	//: invoked unchecked below — both are reachable from caller input and
	//: would crash the process instead of failing the watch. Validate first so
	//: a bad Watch call returns CONFIG_WATCH_FAILED like any other watch fault.
	if err := w.validateInputs(onChange); err != nil {
		//: bad input fails the watch instead of crashing the caller.
		return err
	}
	//: capture the initial fingerprint as the baseline.
	lastMod, lastSize, err := w.stat()
	//: an unstattable target cannot be watched.
	if err != nil {
		//: surface CONFIG_WATCH_FAILED.
		return wrapAs(coreconfig.ConfigWatchFailed, err)
	}
	//: the ticker + loop live in poll so Watch stays within the cyclomatic
	//: budget, and so the ticker channel stays LOCAL to the for-select that
	//: receives from it (KTN-GOROUTINE-CHANRECV-OK).
	return w.poll(ctx, onChange, lastMod, lastSize)
}

// poll owns the ticker and runs the detection loop until ctx is cancelled,
// firing onChange on each observed mtime/size change. Split out of Watch so the
// setup (input validation, baseline stat) and the loop each stay within the
// cyclomatic budget — and so the ticker is created in the same function that
// receives from it.
func (w pollWatcher) poll(ctx context.Context, onChange func(), lastMod, lastSize int64) error {
	//: poll on the configured interval; validateInputs already proved it > 0.
	ticker := time.NewTicker(w.interval)
	//: always release the ticker.
	defer ticker.Stop()
	//: loop until cancellation.
	for {
		//: race the tick against context cancellation.
		select {
		case <-ctx.Done():
			//: cancellation is a clean stop, not an error.
			return nil
		case <-ticker.C:
			//: re-stat and compare against the baseline.
			mod, size, statErr := w.stat()
			//: a stat failure mid-watch aborts.
			if statErr != nil {
				//: surface CONFIG_WATCH_FAILED.
				return wrapAs(coreconfig.ConfigWatchFailed, statErr)
			}
			//: a changed mtime or size fires the callback.
			if mod != lastMod || size != lastSize {
				//: update the baseline + notify.
				lastMod, lastSize = mod, size
				onChange()
			}
		}
	}
}

// validateInputs rejects the two Watch arguments that would otherwise panic:
// a non-positive interval (time.NewTicker panics) and a nil onChange (invoked
// unchecked on the first detected change). Kept out of Watch so the polling
// loop stays within the cyclomatic budget.
func (w pollWatcher) validateInputs(onChange func()) error {
	//: a non-positive poll interval can never produce a tick.
	if w.interval <= 0 {
		//: surface the documented watch sentinel.
		return wrapAs(coreconfig.ConfigWatchFailed, errNonPositiveInterval)
	}
	//: a nil callback would panic on the first detected change.
	if onChange == nil {
		//: refuse rather than deferring the panic.
		return wrapAs(coreconfig.ConfigWatchFailed, errNilOnChange)
	}
	//: inputs are usable.
	return nil
}

// stat returns the target's modification time (nanoseconds) and size.
func (w pollWatcher) stat() (mod, size int64, err error) {
	//: a single os.Stat yields both fields.
	info, statErr := os.Stat(w.path)
	//: propagate the stat error to the caller.
	if statErr != nil {
		//: the file is missing or unreadable.
		return 0, 0, statErr
	}
	//: mtime as Unix nanoseconds + byte size.
	return info.ModTime().UnixNano(), info.Size(), nil
}
