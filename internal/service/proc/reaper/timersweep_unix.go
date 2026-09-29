//go:build unix && !solaris

// Package reaper — no timer sweep where every child's exit posts SIGCHLD.
package reaper

import "time"

// timerSweepEvery is zero: on Linux, darwin and the BSDs every exiting child
// posts SIGCHLD, so the loop sweeps on the signal alone. illumos and Solaris
// are the exception (timersweep_solaris.go).
const timerSweepEvery time.Duration = 0
