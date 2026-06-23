// Package config — cross-OS poll watcher.
package config

import (
	"context"
	"os"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
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
	//: capture the initial fingerprint as the baseline.
	lastMod, lastSize, err := w.stat()
	//: an unstattable target cannot be watched.
	if err != nil {
		//: surface CONFIG_WATCH_FAILED.
		return wrapAs(coreconfig.ConfigWatchFailed, err)
	}
	//: poll on the configured interval.
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
