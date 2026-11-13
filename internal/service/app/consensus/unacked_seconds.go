//go:build darwin || windows

package consensus

import "time"

// unackedSeconds is d in the whole seconds TCP_RXT_CONNDROPTIME and
// TCP_MAXRT count, rounded up and at least one: a bound rounded down to 0
// would be no bound at all.
func unackedSeconds(d time.Duration) int {
	return max(int((d+time.Second-1)/time.Second), 1)
}
