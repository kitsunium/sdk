//go:build unix && !solaris

package reaper

import "time"

// timerSweepEvery is zero: on Linux, darwin and the BSDs every exiting child
// posts SIGCHLD, so the loop sweeps on the signal alone. illumos and Solaris
// are the exception (timersweep_solaris.go).
const timerSweepEvery time.Duration = 0
