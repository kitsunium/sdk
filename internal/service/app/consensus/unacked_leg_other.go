//go:build !darwin && !windows

package consensus

import "time"

// unackedLeg is how long bytes a write handed the kernel may take to reach
// the peer before the kernel aborts their connection: d, TCP_USER_TIMEOUT's
// bound on Linux; the BSDs and illumos set none, and d stands for it there,
// a copy delivered later being fenced by the step function, never applied
// twice (resendWindow, #1482).
func unackedLeg(d time.Duration) time.Duration {
	return d
}
