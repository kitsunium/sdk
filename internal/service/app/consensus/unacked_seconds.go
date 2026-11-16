//go:build darwin || windows

package consensus

import "time"

// unackedSeconds is d in the whole seconds TCP_RXT_CONNDROPTIME and
// TCP_MAXRT count, rounded up and at least one: a bound rounded down to 0
// would be no bound at all.
func unackedSeconds(d time.Duration) int {
	return max(int((d+time.Second-1)/time.Second), 1)
}

// unackedLeg is how long bytes a write handed the kernel may take to reach
// the peer before the kernel aborts their connection: d rounded up to the
// second TCP_RXT_CONNDROPTIME and TCP_MAXRT count in (resendWindow, #1482).
func unackedLeg(d time.Duration) time.Duration {
	return time.Duration(unackedSeconds(d)) * time.Second
}
