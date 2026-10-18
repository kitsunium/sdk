// Package config — hands the external suite the poll watcher on a clock it
// drives, so a test knows when the baseline is taken instead of sleeping on it.
package config

import (
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/app/config"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// PollWatcherOnClockForTest is PollWatcher ticking on clk: its baseline is
// taken before its ticker is armed, so clk.BlockUntil(1) returning means the
// baseline exists, and clk.Advance(interval) is one poll.
func PollWatcherOnClockForTest(path string, interval time.Duration, clk clock.Waiter) coreconfig.Watcher {
	return pollWatcher{path: path, interval: interval, clk: clk}
}
