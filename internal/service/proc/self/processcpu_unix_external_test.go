//go:build unix

package self_test

import (
	"syscall"
	"time"
)

// processCPU is the process's user and system CPU time as getrusage(2)
// reports it, read by the test itself, and whether the call answered.
func processCPU() (time.Duration, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano()), true
}
