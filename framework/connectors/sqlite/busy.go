package sqlite

import (
	"runtime"
	"time"
)

// busyDelays is SQLite's own schedule of busy sleeps (sqliteDefaultBusyCallback,
// HAVE_USLEEP, which modernc.org/sqlite is built with): the first twelve,
// then 100 ms for every further one, the last cut to what is left.
var busyDelays = [...]time.Duration{
	1 * time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
	15 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond, 25 * time.Millisecond,
	25 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond,
}

// busySleeps is how many sleeps SQLite takes before it gives up after budget.
func busySleeps(budget time.Duration) int {
	n := 0
	for prior := time.Duration(0); prior < budget; n++ {
		delay := busyDelays[min(n, len(busyDelays)-1)]
		prior += min(delay, budget-prior)
	}
	return n
}

// sleepOvershoot is what one busy sleep costs past what SQLite asks, on this
// GOOS. SQLite counts the delays it ASKED for, never the time that passed,
// and the driver sleeps with time.Sleep: on OpenBSD and NetBSD, whose clock
// ticks a hundred times a second, every sleep ends on a tick, about 12 ms
// late — a 1.75 s busy timeout measured 2.04 s to 2.10 s there, past the
// call's 2 s deadline (e2e-cross, OpenBSD 7.9 and NetBSD 10.1). Elsewhere the
// overshoot is too small to matter.
func sleepOvershoot() time.Duration {
	switch runtime.GOOS {
	case "openbsd", "netbsd":
		return 12 * time.Millisecond
	}
	return 0
}

// busyTimeout is the busy timeout to hand SQLite so that it gives up within
// wait of WALL time: wait itself where a sleep ends when asked, and where it
// ends late, the longest timeout whose sleeps and their overshoot still fit
// in wait — so kit's promise, SQLite's refusal before the call's own
// deadline (ADR 0178 C2), holds on every kernel.
func busyTimeout(wait time.Duration) time.Duration {
	return busyTimeoutWith(wait, sleepOvershoot())
}

// busyTimeoutWith is busyTimeout for a given overshoot.
func busyTimeoutWith(wait, overshoot time.Duration) time.Duration {
	if overshoot <= 0 {
		return wait
	}
	budget := wait.Truncate(time.Millisecond)
	for budget > time.Millisecond && budget+time.Duration(busySleeps(budget))*overshoot > wait {
		budget -= time.Millisecond
	}
	return budget
}
